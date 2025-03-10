package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	apidata "github.com/opst/knitfab-api-types/data"
	"github.com/opst/knitfab-api-types/misc/rfctime"
	"github.com/opst/knitfab-api-types/plans"
	"github.com/opst/knitfab-api-types/runs"
	apitag "github.com/opst/knitfab-api-types/tags"
	"github.com/opst/knitfab/cmd/knitd_backend/handlers"
	mockkeyprovider "github.com/opst/knitfab/cmd/knitd_backend/provider/keyProvider/mockKeyprovider"
	httptestutil "github.com/opst/knitfab/internal/testutils/http"
	binddata "github.com/opst/knitfab/pkg/api-types-binding/data"
	"github.com/opst/knitfab/pkg/domain"
	dbdatamock "github.com/opst/knitfab/pkg/domain/data/db/mock"
	"github.com/opst/knitfab/pkg/domain/data/k8s/dataagt"
	k8sdatamocks "github.com/opst/knitfab/pkg/domain/data/k8s/mock"
	mockDataK8s "github.com/opst/knitfab/pkg/domain/data/k8s/mock"
	kerr "github.com/opst/knitfab/pkg/domain/errors"
	k8serrors "github.com/opst/knitfab/pkg/domain/errors/k8serrors"
	keychain "github.com/opst/knitfab/pkg/domain/keychain/k8s"
	"github.com/opst/knitfab/pkg/domain/keychain/k8s/key"
	mockkeychain "github.com/opst/knitfab/pkg/domain/keychain/k8s/mock"
	dbrunmock "github.com/opst/knitfab/pkg/domain/run/db/mock"
	"github.com/opst/knitfab/pkg/utils/cmp"
	"github.com/opst/knitfab/pkg/utils/try"
)

func TestGetDataHandler(t *testing.T) {
	type when struct {
		knitId          string
		respFromDataAgt responseDescriptor
	}

	type testcase struct {
		when when
	}

	for name, testcase := range map[string]testcase{
		"GetData should proxy request & response: code 200, chunked and with trailer": {
			when: when{
				knitId: "test-knit-id",
				respFromDataAgt: responseDescriptor{
					code: 200,
					header: map[string][]string{
						"Content-Type":           {"text/plain"},
						"Transfer-Encoding":      {"chunked"},
						"Trailer":                {"Example-Trailer"},
						"X-Some-Header-For-Test": {"aaa", "bbb"},
					},
					body: []byte("quick brown fox jumps over a lazy dog"),
					trailer: &map[string][]string{
						"Example-Trailer": {"example trailer payload"},
					},
				},
			},
		},
		"GetData should proxy request & response: code 200, not chunked, no trailer": {
			when: when{
				knitId: "test-knit-id",
				respFromDataAgt: responseDescriptor{
					code: 200,
					header: map[string][]string{
						"Content-Type":           {"text/plain"},
						"X-Some-Header-For-Test": {"aaa", "bbb"},
					},
					body: []byte("quick brown fox jumps over a lazy dog"),
				},
			},
		},
		"GetData should proxy request & response: code 400, with trailer": {
			when: when{
				knitId: "test-knit-id",
				respFromDataAgt: responseDescriptor{
					code: 400,
					header: map[string][]string{
						"Content-Type":           {"application/json"},
						"Transfer-Encoding":      {"chunked"},
						"Trailer":                {"Example-Trailer"},
						"X-Some-Header-For-Test": {"aaa", "bbb"},
					},
					body: []byte(`{"message": "fake error."}`),
					trailer: &map[string][]string{
						"Example-Trailer": {"example trailer payload"},
					},
				},
			},
		},
		"GetData should proxy request & response: code 400, not chunked, no trailer": {
			when: when{
				knitId: "test-knit-id",
				respFromDataAgt: responseDescriptor{
					code: 400,
					header: map[string][]string{
						"Content-Type":           {"application/json"},
						"X-Some-Header-For-Test": {"aaa", "bbb"},
					},
					body: []byte(`{"message": "fake error."}`),
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var actualRequest *requestSnapshot
			hdr := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				actualRequest = try.To(Read(r)).OrFatal(t)
				testcase.when.respFromDataAgt.Write(w)
			})

			svr := httptest.NewServer(hdr)
			defer svr.Close()

			targetData := domain.KnitData{
				KnitDataBody: domain.KnitDataBody{
					KnitId:    testcase.when.knitId,
					VolumeRef: "volume-ref",
				},
			}

			mDataInterface := dbdatamock.NewDataInterface()
			mDataInterface.Impl.Get = func(ctx context.Context, knitIds []string) (map[string]domain.KnitData, error) {
				return map[string]domain.KnitData{targetData.KnitId: targetData}, nil
			}

			dbDataAgent := domain.DataAgent{
				Name:         "fake-data-agent",
				Mode:         domain.DataAgentRead,
				KnitDataBody: targetData.KnitDataBody,
			}
			mDataInterface.Impl.NewAgent = func(_ context.Context, _ string, mode domain.DataAgentMode, _ time.Duration) (domain.DataAgent, error) {
				if mode != domain.DataAgentRead {
					t.Errorf(
						"NewAgent should be called with DataAgentModeRead. actual = %s", mode,
					)
				}
				return dbDataAgent, nil
			}
			mDataInterface.Impl.RemoveAgent = func(ctx context.Context, name string) error {
				return nil
			}

			dagt := NewMockedDataagt(svr)
			dagt.Impl.Close = func() error { return nil }

			mDataK8s := mockDataK8s.New(t)
			spawnDataagtCalled := 0
			mDataK8s.Impl.SpawnDataAgent = func(ctx context.Context, d domain.DataAgent, deadline time.Time) (dataagt.DataAgent, error) {
				spawnDataagtCalled += 1

				if !d.Equal(&dbDataAgent) {
					t.Errorf("knitId unmatch. (actual, expected) = (%#v, %#v)", d, targetData)
				}

				if mDataInterface.Calls.NewAgent.Times() < 1 {
					t.Error("NewAgent should be called before spawning DataAgent")
				}

				return dagt, nil
			}

			e := echo.New()
			ectx, resprec := httptestutil.Get(
				e, path.Join("/api/backends/data", testcase.when.knitId),
				httptestutil.WithHeader("X-User-Custom-Header", "aaaa"),
			)
			ectx.SetPath("/api/backends/data/:knitId")
			ectx.SetParamNames("knitId")
			ectx.SetParamValues("test-knit-id")

			testee := handlers.GetDataHandler(mDataInterface, mDataK8s, "knitId")
			err := testee(ectx)
			if err != nil {
				t.Fatalf("testee returns error unexpectedly. %v", err)
			}

			// --- about request ---
			expectedReqHeader := map[string][]string{
				"x-user-custom-header": {"aaaa"},
			}
			if !cmp.MapGeqWith(
				actualRequest.header, expectedReqHeader, cmp.SliceContentEq[string],
			) {
				t.Errorf(
					"sent header is not proxied to dataagt. (actual, expected) = (%+v, %+v)",
					actualRequest.header, expectedReqHeader,
				)
			}
			if 0 < len(actualRequest.body) {
				t.Errorf("unexpected payload is sent to dataagt. payload = %s", actualRequest.body)
			}
			if 0 < len(actualRequest.trailer) {
				t.Errorf("unexpected trailer is sent to dataagt. trailers = %+v", actualRequest.trailer)
			}

			// --- about dataagt resource management ---
			if spawnDataagtCalled < 1 {
				t.Fatalf("spawnDataAgt called too many/less. actual = %d != 1", spawnDataagtCalled)
			}
			if dagt.Calls.Close.Times() < 1 {
				t.Errorf("dataagt.Close has not been called")
			}

			if mDataInterface.Calls.RemoveAgent.Times() < 1 {
				t.Errorf("DataInterface.RemoveAgent has not been called")
			}

			// --- about response ---
			if match, err := ResponseEq(*resprec, testcase.when.respFromDataAgt); err != nil {
				t.Errorf("failed to load from actual response. %v", err)
			} else if !match.Match() {
				t.Errorf("response is not copy of dataagt's. %+v", match)
			}
		})
	}

	for name, testcase := range map[string]struct {
		errorFromNewAgent  error
		errorFromSpawner   error
		expectedStatusCode int
	}{
		"ErrMissing error causes 404": {
			errorFromNewAgent:  kerr.ErrMissing,
			expectedStatusCode: http.StatusNotFound,
		},
		"ErrDeadlineExceeded error causes 503 error": {
			errorFromSpawner:   k8serrors.ErrDeadlineExceeded,
			expectedStatusCode: http.StatusServiceUnavailable,
		},
		"other errors causes 500 error": {
			errorFromSpawner:   errors.New("fake error"),
			expectedStatusCode: http.StatusInternalServerError,
		},
	} {
		t.Run("when it faces "+name, func(t *testing.T) {
			knitId := "test-knit-id"

			mDataInterface := dbdatamock.NewDataInterface()
			mDataInterface.Impl.NewAgent = func(ctx context.Context, knitId string, mode domain.DataAgentMode, timeout time.Duration) (domain.DataAgent, error) {
				if testcase.errorFromNewAgent != nil {
					return domain.DataAgent{}, testcase.errorFromNewAgent
				}
				return domain.DataAgent{
					Name: "fake-data-agent",
					Mode: mode,
					KnitDataBody: domain.KnitDataBody{
						KnitId: knitId, VolumeRef: "volume-ref",
						Tags: domain.NewTagSet([]domain.Tag{
							{Key: "knit#id", Value: knitId},
							{Key: "knit#timestamp", Value: "2022-01-02T12:23:34+00:00"},
							{Key: "some-user-defined-tag", Value: "tag value"},
						}),
					},
				}, nil
			}

			mDataK8s := k8sdatamocks.New(t)
			mDataK8s.Impl.SpawnDataAgent = func(context.Context, domain.DataAgent, time.Time) (dataagt.DataAgent, error) {
				// No mock dataagts are need.
				// This API should not access DataAgent because of the caused error.
				// If SEGV, that is bug.
				return nil, testcase.errorFromSpawner
			}

			testee := handlers.GetDataHandler(mDataInterface, mDataK8s, "knitId")

			e := echo.New()
			ectx, resprec := httptestutil.Get(e, "/api/backends/data/"+knitId)
			ectx.SetPath("/api/backends/data/:knitId")
			ectx.SetParamNames("knitId")
			ectx.SetParamValues("test-knit-id")

			err := testee(ectx)
			if err == nil {
				t.Fatalf("GetDataAgt handler does not error when dataagt is not up. resp = %+v", resprec)
			}
			if httperr := new(echo.HTTPError); !errors.As(err, &httperr) {
				t.Errorf("error is not echo.HTTPError. actual = %+v", err)
			} else if httperr.Code != testcase.expectedStatusCode {
				t.Errorf("error code is not %d. actual = %d", testcase.expectedStatusCode, httperr.Code)
			}
		})
	}

	t.Run("when dataagt is broken, response 500", func(t *testing.T) {
		knitId := "test-knit-id"
		data := domain.KnitData{
			KnitDataBody: domain.KnitDataBody{
				KnitId: knitId, VolumeRef: "#volume-ref",
				Tags: domain.NewTagSet([]domain.Tag{
					{Key: domain.KeyKnitId, Value: knitId},
				}),
			},
			Upsteram: domain.DataSource{
				RunBody: domain.RunBody{
					Id: "run#1", Status: domain.Done,
					UpdatedAt: try.To(rfctime.ParseRFC3339DateTime(
						"2022-10-11T12:13:14+00:00",
					)).OrFatal(t).Time(),
					PlanBody: domain.PlanBody{
						Pseudo: &domain.PseudoPlanDetail{
							Name: domain.Uploaded,
						},
					},
				},
				MountPoint: &domain.MountPoint{Id: 42, Path: "/out"},
			},
		}

		mDataInterface := dbdatamock.NewDataInterface()
		mDataInterface.Impl.Get = func(context.Context, []string) (map[string]domain.KnitData, error) {
			return map[string]domain.KnitData{
				data.KnitId: data,
			}, nil
		}
		daRecord := domain.DataAgent{
			Name:         "fake-data-agent",
			Mode:         domain.DataAgentRead,
			KnitDataBody: data.KnitDataBody,
		}
		mDataInterface.Impl.NewAgent = func(_ context.Context, knitId string, dam domain.DataAgentMode, deadline time.Duration) (domain.DataAgent, error) {
			if dam != domain.DataAgentRead {
				t.Errorf("NewAgent should be called with DataAgentModeRead. actual = %s", dam)
			}
			if knitId != data.KnitId {
				t.Errorf("NewAgent should be called with KnitId = %s. actual = %s", data.KnitId, knitId)
			}
			return daRecord, nil
		}
		mDataInterface.Impl.RemoveAgent = func(_ context.Context, name string) error {
			if name != daRecord.Name {
				t.Errorf("unexpected dataagt name. actual = %s, expected = %s", name, daRecord.Name)
			}
			return nil
		}
		dagt := NewBrokenDataagt()
		dagt.Impl.Close = func() error { return nil }

		mDataK8s := mockDataK8s.New(t)
		mDataK8s.Impl.SpawnDataAgent = func(context.Context, domain.DataAgent, time.Time) (dataagt.DataAgent, error) {
			return dagt, nil
		}

		testee := handlers.GetDataHandler(mDataInterface, mDataK8s, "knitId")

		e := echo.New()
		ectx, resprec := httptestutil.Get(e, "/api/backends/data/"+knitId)
		ectx.SetPath("/api/backends/data/:knitId")
		ectx.SetParamNames("knitId")
		ectx.SetParamValues("test-knit-id")

		err := testee(ectx)
		if err == nil {
			t.Fatalf("GetDataAgt handler does not error when dataagt is not up. resp = %+v", resprec)
		}
		if httperr := new(echo.HTTPError); !errors.As(err, &httperr) {
			t.Errorf("error is not echo.HTTPError. actual = %+v", err)
		} else if httperr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, httperr.Code)
		}

		if dagt.Calls.URL.Times() < 1 {
			t.Errorf("URL of dataagt is not queried.")
		}
	})

}

func TestPostDataHandler(t *testing.T) {

	type when struct {
		knitId          string
		respFromDataAgt responseDescriptor
	}

	type testcase struct {
		when when
	}

	for name, testcase := range map[string]testcase{
		"chunked and with trailer": {
			when: when{
				knitId: "test-knit-id",
				respFromDataAgt: responseDescriptor{
					code: 200,
					header: map[string][]string{
						"Content-Type":           {"text/plain"},
						"Transfer-Encoding":      {"chunked"},
						"Trailer":                {"Example-Trailer"},
						"X-Some-Header-For-Test": {"aaa", "bbb"},
					},
					body: []byte("quick brown fox jumps over a lazy dog"),
					trailer: &map[string][]string{
						"Example-Trailer": {"example trailer payload"},
					},
				},
			},
		},
		"not chunked, no trailer": {
			when: when{
				knitId: "test-knit-id",
				respFromDataAgt: responseDescriptor{
					code: 200,
					header: map[string][]string{
						"Content-Type":           {"text/plain"},
						"X-Some-Header-For-Test": {"aaa", "bbb"},
					},
					body: []byte("quick brown fox jumps over a lazy dog"),
				},
			},
		},
	} {
		t.Run("PostData should proxy request and success response with KnitData info: "+name, func(t *testing.T) {
			var actualRequest *requestSnapshot
			hdr := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				actualRequest = try.To(Read(r)).OrFatal(t)
				testcase.when.respFromDataAgt.Write(w)
			})

			svr := httptest.NewServer(hdr)
			defer svr.Close()

			knitId := "test-knit-id"
			pvcname := "test-pvc-name"
			runId := "pseudo-run"
			planId := "test-plan-id"

			createdData := domain.KnitData{
				KnitDataBody: domain.KnitDataBody{
					KnitId:    knitId,
					VolumeRef: pvcname,
					Tags: domain.NewTagSet([]domain.Tag{
						{Key: "knit#id", Value: knitId},
						{Key: "knit#timestamp", Value: "2022-01-02T12:23:34+00:00"},
						{Key: "some-user-defined-tag", Value: "tag value"},
					}),
				},
				Upsteram: domain.DataSource{
					RunBody: domain.RunBody{
						Id: runId, Status: domain.Done,
						UpdatedAt: try.To(
							rfctime.ParseRFC3339DateTime("2022-01-02T12:23:34+00:00"),
						).OrFatal(t).Time(),
						PlanBody: domain.PlanBody{
							PlanId: planId, Hash: "#hash", Active: true,
							Pseudo: &domain.PseudoPlanDetail{Name: domain.Uploaded},
						},
					},
					MountPoint: &domain.MountPoint{Id: 1, Path: "/out"},
				},
			}

			dagt := NewMockedDataagt(svr)
			dagt.Impl.KnitID = func() string { return knitId }
			dagt.Impl.Close = func() error { return nil }
			dagt.Impl.VolumeRef = func() string { return pvcname }

			createdRun := domain.Run{
				RunBody: domain.RunBody{
					Id:         runId,
					Status:     domain.Running,
					WorkerName: "",
					UpdatedAt:  time.Time(try.To(rfctime.ParseRFC3339DateTime("2023-10-30T12:34:56+00:00")).OrFatal(t)),
					PlanBody: domain.PlanBody{
						PlanId: string(domain.Uploaded),
						Hash:   "#plan",
						Active: true,
						Pseudo: &domain.PseudoPlanDetail{
							Name: domain.Uploaded,
						},
					},
				},
				Outputs: []domain.Assignment{
					{
						MountPoint:   domain.MountPoint{Id: 1, Path: "/out"},
						KnitDataBody: createdData.KnitDataBody,
					},
				},
			}
			iRunDB := dbrunmock.NewRunInterface()
			iRunDB.Impl.NewPseudo = func(_ context.Context, planName domain.PseudoPlanName, _ time.Duration) (string, error) {
				if planName != domain.Uploaded {
					t.Errorf("unexpected plan name. actual = %s, expected = %s", planName, domain.Uploaded)
				}
				return runId, nil
			}
			iRunDB.Impl.Get = func(context.Context, []string) (map[string]domain.Run, error) {
				return map[string]domain.Run{runId: createdRun}, nil
			}
			iRunDB.Impl.SetStatus = func(context.Context, string, domain.KnitRunStatus) error {
				return nil
			}
			iRunDB.Impl.Finish = func(context.Context, string) error { return nil }

			iDataDB := dbdatamock.NewDataInterface()
			iDataDB.Impl.Get = func(context.Context, []string) (map[string]domain.KnitData, error) {
				return map[string]domain.KnitData{knitId: createdData}, nil
			}
			dbDataAgent := domain.DataAgent{
				Name:         "fake-data-agent",
				Mode:         domain.DataAgentWrite,
				KnitDataBody: createdData.KnitDataBody,
			}
			iDataDB.Impl.NewAgent = func(context.Context, string, domain.DataAgentMode, time.Duration) (domain.DataAgent, error) {
				return dbDataAgent, nil
			}
			iDataDB.Impl.RemoveAgent = func(context.Context, string) error {
				return nil
			}

			iDataK8s := k8sdatamocks.New(t)
			spawnDataagtCalled := 0
			iDataK8s.Impl.SpawnDataAgent = func(ctx context.Context, da domain.DataAgent, deadline time.Time) (dataagt.DataAgent, error) {
				spawnDataagtCalled += 1
				if !dbDataAgent.Equal(&da) {
					t.Errorf(
						"SpawnDataAgent called with DataAgent:\n===actual===\n%+v\n===expected===\n%+v",
						da, dbDataAgent,
					)
				}

				if !cmp.SliceContentEqWith(
					iDataDB.Calls.NewAgent,
					[]struct {
						KnitId string
						Mode   domain.DataAgentMode
					}{{KnitId: knitId, Mode: domain.DataAgentWrite}},
					func(a struct {
						KnitId                string
						Mode                  domain.DataAgentMode
						LifecycleSuspendUntil time.Duration
					}, b struct {
						KnitId string
						Mode   domain.DataAgentMode
					}) bool {
						return a.KnitId == b.KnitId && a.Mode == b.Mode && deadline.Before(time.Now().Add(a.LifecycleSuspendUntil))
					},
				) {
					t.Errorf(
						"DataInterface.NewAgent should be called before spawning DataAgent:\n===actual calls===\n%+v\n===DataAgent is spawned as===\n%+v\ndeadline: %s",
						iDataDB.Calls.NewAgent, da, deadline,
					)
				}

				return dagt, nil
			}

			e := echo.New()
			payload := []byte("arbitary byte stream...")
			ectx, resprec := httptestutil.Post(
				e, "/api/backends/data/", bytes.NewBuffer(payload),
				httptestutil.ContentType("example/test-data"),
				httptestutil.Chunked(),
				httptestutil.WithHeader("x-custom-header", "header-value", "header-value-2"),
				httptestutil.WithTrailer("x-usersending-trailer", "trailer-value", "trailer-value-2"),
			)
			ectx.SetPath("/api/backends/data/")

			testee := handlers.PostDataHandler(iDataDB, iRunDB, iDataK8s)
			if err := testee(ectx); err != nil {
				t.Fatalf("testee returns error unexpectedly. %v", err)
			}

			// --- about request ---

			expectedReqHeader := map[string][]string{
				"content-type":    {"example/test-data"},
				"x-custom-header": {"header-value", "header-value-2"},
				// not test below: these are hop-by-hop header.
				//    see: https://datatracker.ietf.org/doc/html/rfc2616#section-13.5.1
				// "transfer-encoding": {"chunked"},
				// "trailer":           {"x-usersending-trailer"},
			}
			if !cmp.MapGeqWith(
				actualRequest.header, expectedReqHeader, cmp.SliceContentEq[string],
			) {
				t.Errorf(
					"requested headers are not proxied to dataagt. (actual, expected) = (%+v, %+v)",
					actualRequest.header, expectedReqHeader,
				)
			}
			if !actualRequest.chunked {
				t.Errorf("request dataagt received is not chunked.")
			}

			if !bytes.Equal(actualRequest.body, payload) {
				t.Errorf(
					"requested payload is not proxied to dataagt. (actual, expected) = (%s, %s)",
					actualRequest.body, payload,
				)
			}

			expectedReqTrailer := map[string][]string{
				"x-usersending-trailer": {"trailer-value", "trailer-value-2"},
			}
			if !cmp.MapGeqWith(
				actualRequest.trailer, expectedReqTrailer, cmp.SliceContentEq[string],
			) {
				t.Errorf(
					"requested trailers are not proxied to dataagt. (acutual, expected) = (%+v, %+v)",
					actualRequest.trailer, expectedReqTrailer,
				)
			}

			// --- dataagt management ---
			if spawnDataagtCalled < 1 {
				t.Fatalf("spawnDataAgt called too many/less. actual = %d != 1", spawnDataagtCalled)
			}
			if dagt.Calls.Close.Times() < 1 {
				t.Errorf("dataagt.Close has not been called.")
			}

			// --- about data access ---
			{
				if iRunDB.Calls.NewPseudo.Times() < 1 {
					t.Errorf("RunInterface.NewPseudo has not been called.")
				}
			}
			{
				expected := []struct {
					RunId     string
					NewStatus domain.KnitRunStatus
				}{
					{RunId: runId, NewStatus: domain.Completing},
				}
				actual := iRunDB.Calls.SetStatus
				if !cmp.SliceContentEq(expected, actual) {
					t.Errorf(
						"RunInterface.SetStatus\n===actual===\n%+v\n===expected\n%+v",
						actual, expected,
					)
				}
			}
			{
				expected := []string{runId}
				if !cmp.SliceEq(iRunDB.Calls.Finish, expected) {
					t.Errorf(
						"unmatch: invoke RunInterface.Finish:\n===actual===\n%+v\n===expected===\n%+v",
						iRunDB.Calls.Finish, expected,
					)
				}
			}
			if !cmp.SliceEqWith(
				iDataDB.Calls.Get,
				[]struct{ KnitId []string }{{KnitId: []string{knitId}}},
				func(a, b struct{ KnitId []string }) bool {
					return cmp.SliceContentEq(a.KnitId, b.KnitId)
				},
			) {
				t.Error("DataInterface.Get has not been called.")
			}

			if iDataDB.Calls.RemoveAgent.Times() < 1 {
				t.Errorf("DataInterface.RemoveAgent has not been called")
			}

			// --- about response ---
			expectedResponsePayload := apidata.Detail{
				KnitId: knitId,
				Tags: []apitag.Tag{
					{Key: "knit#id", Value: knitId},
					{Key: "knit#timestamp", Value: "2022-01-02T12:23:34+00:00"},
					{Key: "some-user-defined-tag", Value: "tag value"},
				},
				Upstream: apidata.CreatedFrom{
					Run: runs.Summary{
						RunId: runId, Status: string(domain.Done),
						UpdatedAt: try.To(rfctime.ParseRFC3339DateTime(
							"2022-01-02T12:23:34+00:00",
						)).OrFatal(t),
						Plan: plans.Summary{PlanId: planId, Name: string(domain.Uploaded)},
					},
					Mountpoint: &plans.Mountpoint{Path: "/out"},
				},
			}

			var actualPayload apidata.Detail
			rawRespPayload := try.To(io.ReadAll(resprec.Result().Body)).OrFatal(t)
			if err := json.Unmarshal(rawRespPayload, &actualPayload); err != nil {
				t.Fatalf("Response is not json. err = %v", err)
			}

			if !actualPayload.Equal(expectedResponsePayload) {
				t.Errorf(
					"knitId in response is wrong\n===actual===\n%s\n===expected===\n%s",
					try.To(json.MarshalIndent(actualPayload, "", "  ")).OrFatal(t),
					try.To(json.MarshalIndent(expectedResponsePayload, "", "  ")).OrFatal(t),
				)
			}
		})
	}

	for name, testcase := range map[string]testcase{
		"dataagt error: code 400, chunked and with trailer": {
			when: when{
				knitId: "test-knit-id",
				respFromDataAgt: responseDescriptor{
					code: 400,
					header: map[string][]string{
						"Content-Type":           {"text/plain"},
						"Transfer-Encoding":      {"chunked"},
						"Trailer":                {"Example-Trailer"},
						"X-Some-Header-For-Test": {"aaa", "bbb"},
					},
					body: []byte("quick brown fox jumps over a lazy dog"),
					trailer: &map[string][]string{
						"Example-Trailer": {"example trailer payload"},
					},
				},
			},
		},
		"dataagt error: code 500, not chunked, no trailer": {
			when: when{
				knitId: "test-knit-id",
				respFromDataAgt: responseDescriptor{
					code: 500,
					header: map[string][]string{
						"Content-Type":           {"text/plain"},
						"X-Some-Header-For-Test": {"aaa", "bbb"},
					},
					body: []byte("quick brown fox jumps over a lazy dog"),
				},
			},
		},
	} {
		t.Run("PostData should proxy request, and also proxy error response: "+name, func(t *testing.T) {
			hdr := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				testcase.when.respFromDataAgt.Write(w)
			})

			svr := httptest.NewServer(hdr)
			defer svr.Close()

			knitId := "test-knit-id"
			pvcname := "test-pvc-name"
			runId := "example-run-id"

			dagt := NewMockedDataagt(svr)
			dagt.Impl.KnitID = func() string { return knitId }
			// dataagt.Impl.Commit = func() {} // no success, no commit.
			dagt.Impl.Close = func() error { return nil }
			dagt.Impl.VolumeRef = func() string { return pvcname }

			createdRun := domain.Run{
				RunBody: domain.RunBody{
					Id:         runId,
					Status:     domain.Running,
					WorkerName: "",
					UpdatedAt:  time.Time(try.To(rfctime.ParseRFC3339DateTime("2023-10-30T12:34:56+00:00")).OrFatal(t)),
					PlanBody: domain.PlanBody{
						PlanId: string(domain.Uploaded),
						Hash:   "#plan",
						Active: true,
						Pseudo: &domain.PseudoPlanDetail{
							Name: domain.Uploaded,
						},
					},
				},
				Outputs: []domain.Assignment{
					{
						MountPoint: domain.MountPoint{Id: 1, Path: "/out"},
						KnitDataBody: domain.KnitDataBody{
							KnitId:    knitId,
							VolumeRef: pvcname,
							Tags: domain.NewTagSet([]domain.Tag{
								{Key: "knit#id", Value: knitId},
								{Key: "knit#timestamp", Value: "2022-01-02T12:23:34+00:00"},
								{Key: "some-user-defined-tag", Value: "tag value"},
							}),
						},
					},
				},
			}
			iRunDB := dbrunmock.NewRunInterface()
			iRunDB.Impl.NewPseudo = func(_ context.Context, planName domain.PseudoPlanName, _ time.Duration) (string, error) {
				if planName != domain.Uploaded {
					t.Errorf("unexpected plan name. actual = %s, expected = %s", planName, domain.Uploaded)
				}
				return runId, nil
			}
			iRunDB.Impl.Get = func(context.Context, []string) (map[string]domain.Run, error) {
				return map[string]domain.Run{createdRun.RunBody.Id: createdRun}, nil
			}
			iRunDB.Impl.SetStatus = func(context.Context, string, domain.KnitRunStatus) error {
				return nil
			}
			iRunDB.Impl.Finish = func(context.Context, string) error { return nil }

			dbDataAgent := domain.DataAgent{
				Name:         "fake-data-agent",
				Mode:         domain.DataAgentWrite,
				KnitDataBody: createdRun.Outputs[0].KnitDataBody,
			}
			iDataDB := dbdatamock.NewDataInterface()
			iDataDB.Impl.NewAgent = func(context.Context, string, domain.DataAgentMode, time.Duration) (domain.DataAgent, error) {
				return dbDataAgent, nil
			}
			iDataDB.Impl.RemoveAgent = func(context.Context, string) error {
				return nil
			}

			spawnDataagtCalled := 0
			iDataK8s := k8sdatamocks.New(t)
			iDataK8s.Impl.SpawnDataAgent = func(ctx context.Context, da domain.DataAgent, deadline time.Time) (dataagt.DataAgent, error) {
				spawnDataagtCalled += 1
				if !dbDataAgent.Equal(&da) {
					t.Errorf(
						"SpawnDataAgent called with DataAgent:\n===actual===\n%+v\n===expected===\n%+v",
						da, dbDataAgent,
					)
				}

				return dagt, nil
			}

			e := echo.New()
			payload := []byte("arbitary byte stream...")
			ectx, resprec := httptestutil.Post(
				e, "/api/backends/data/", bytes.NewBuffer(payload),
				httptestutil.ContentType("example/test-data"),
				httptestutil.Chunked(),
				httptestutil.WithHeader("x-custom-header", "header-value", "header-value-2"),
				httptestutil.WithTrailer("x-usersending-trailer", "trailer-value", "trailer-value-2"),
			)
			ectx.SetPath("/api/backends/data/")

			testee := handlers.PostDataHandler(iDataDB, iRunDB, iDataK8s)
			if err := testee(ectx); err != nil {
				t.Fatalf("testee returns error unexpectedly. %v", err)
			}

			// --- about request ---
			// such test case has been done in code 200 cases.

			// --- dataagt management ---
			if spawnDataagtCalled < 1 {
				t.Fatalf("spawnDataAgt called too many/less. actual = %d != 1", spawnDataagtCalled)
			}
			if dagt.Calls.Close.Times() < 1 {
				t.Errorf("dataagt.Close has not been called.")
			}

			// --- about data access ---
			{
				if iRunDB.Calls.NewPseudo.Times() < 1 {
					t.Errorf("RunInterface.NewPseudo has not been called.")
				}
			}

			{
				expected := []struct {
					RunId     string
					NewStatus domain.KnitRunStatus
				}{
					{RunId: runId, NewStatus: domain.Aborting},
				}
				actual := iRunDB.Calls.SetStatus
				if !cmp.SliceContentEq(expected, actual) {
					t.Errorf(
						"RunInterface.SetStatus\n===actual===\n%+v\n===expected===\n%+v",
						actual, expected,
					)
				}
			}

			{
				expected := []struct {
					RunId     string
					NewStatus domain.KnitRunStatus
				}{
					{
						RunId:     runId,
						NewStatus: domain.Aborting,
					},
				}
				if !cmp.SliceEq(iRunDB.Calls.SetStatus, expected) {
					t.Errorf(
						"DataInterface.SetStatus:\n===actual===\n%+v\n===expected===\n%+v",
						iRunDB.Calls.SetStatus, expected,
					)
				}
				if !cmp.SliceEq(iRunDB.Calls.Finish, []string{runId}) {
					t.Errorf("DataInterface.Finalize should be called.")
				}
			}

			// --- about response ---
			if match, err := ResponseEq(*resprec, testcase.when.respFromDataAgt); err != nil {
				t.Errorf("failed to load from actual response. %v", err)
			} else if !match.Match() {
				t.Errorf("response is not copy of dataagt's. %+v", match)
			}
		})
	}

	for name, testcase := range map[string]struct {
		errorValue         error
		expectedStatusCode int
	}{
		"Conflict error causes 503 error": {
			// Because, Conflict in POST /api/backend/data is caused by corrision of servive/deployent name.
			// Sinse names of service/deployment are generated randomly, also Conflict occurs randomly.
			// It is purely server-side probrem. It should response 5xx status code.
			// User can replay the request, and then it work well, so it is temporal error. It should be 503.
			errorValue:         k8serrors.NewConflict("fake"),
			expectedStatusCode: http.StatusServiceUnavailable,
		},
		"other errors causes 500 error": {
			errorValue:         errors.New("fake error"),
			expectedStatusCode: http.StatusInternalServerError,
		},
	} {
		t.Run("When spawnDataWriter caused "+name, func(t *testing.T) {

			runId := "run-id"
			knitId := "test-knit-id"

			databody := domain.KnitDataBody{
				KnitId:    knitId,
				VolumeRef: "volume-ref",
			}

			iRunDB := dbrunmock.NewRunInterface()
			iRunDB.Impl.NewPseudo = func(context.Context, domain.PseudoPlanName, time.Duration) (string, error) {
				return runId, nil
			}
			iRunDB.Impl.Get = func(context.Context, []string) (map[string]domain.Run, error) {
				run := domain.Run{
					RunBody: domain.RunBody{
						Id: runId,
					},
					Outputs: []domain.Assignment{{KnitDataBody: databody}},
				}
				return map[string]domain.Run{run.RunBody.Id: run}, nil
			}
			iRunDB.Impl.SetStatus = func(context.Context, string, domain.KnitRunStatus) error {
				return nil
			}
			iRunDB.Impl.Finish = func(ctx context.Context, runId string) error {
				return nil
			}

			iDataDB := dbdatamock.NewDataInterface()
			iDataDB.Impl.NewAgent = func(context.Context, string, domain.DataAgentMode, time.Duration) (domain.DataAgent, error) {
				return domain.DataAgent{
					Name:         "fake-data-agent",
					Mode:         domain.DataAgentWrite,
					KnitDataBody: databody,
				}, nil
			}

			iDataK8s := k8sdatamocks.New(t)
			iDataK8s.Impl.SpawnDataAgent = func(context.Context, domain.DataAgent, time.Time) (dataagt.DataAgent, error) {
				return nil, testcase.errorValue
			}
			testee := handlers.PostDataHandler(iDataDB, iRunDB, iDataK8s)

			e := echo.New()
			ectx, resprec := httptestutil.Post(e, "/api/backends/data/", bytes.NewBuffer([]byte("n/a")))

			err := testee(ectx)
			if err == nil {
				t.Fatalf("PostDataHandler does not cause error. resp = %+v", resprec)
			}

			if !errors.Is(err, testcase.errorValue) {
				t.Errorf("root error is not propageted")
			}

			if httperr := new(echo.HTTPError); !errors.As(err, &httperr) {
				t.Errorf("error is not echo.HTTPError. actual = %+v", err)
			} else {
				if httperr.Code != testcase.expectedStatusCode {
					t.Errorf(
						"error code is not %d. actual = %d",
						testcase.expectedStatusCode, httperr.Code,
					)
				}
			}

			{
				actual := iRunDB.Calls.SetStatus
				expected := []struct {
					RunId     string
					NewStatus domain.KnitRunStatus
				}{
					{RunId: runId, NewStatus: domain.Aborting},
				}
				if !cmp.SliceContentEq(actual, expected) {
					t.Errorf(
						"RunInterface.SetStatus\n===actual===\n%+v\n===expected===\n%+v",
						actual, expected,
					)
				}
			}
			{
				actual := iRunDB.Calls.Finish
				expected := []string{runId}
				if !cmp.SliceContentEq(actual, expected) {
					t.Errorf(
						"RunInterface.Finish\n===actual===\n%+v\n===expected===\n%+v",
						actual, expected,
					)
				}
			}
		})
	}

	t.Run("when dataagt is broken, response 500", func(t *testing.T) {

		knitId := "test-knit-id"
		pvcname := "test-pvc-name"
		runId := "test-run-id"

		dagt := NewBrokenDataagt()
		dagt.Impl.VolumeRef = func() string { return pvcname }
		dagt.Impl.Close = func() error { return nil }

		databody := domain.KnitDataBody{
			KnitId:    knitId,
			VolumeRef: pvcname,
		}
		iRunDB := dbrunmock.NewRunInterface()
		iRunDB.Impl.NewPseudo = func(context.Context, domain.PseudoPlanName, time.Duration) (string, error) {
			return runId, nil
		}
		iRunDB.Impl.Get = func(context.Context, []string) (map[string]domain.Run, error) {
			run := domain.Run{
				RunBody: domain.RunBody{
					Id: runId,
				},
				Outputs: []domain.Assignment{{KnitDataBody: databody}},
			}
			return map[string]domain.Run{runId: run}, nil
		}
		iRunDB.Impl.SetStatus = func(context.Context, string, domain.KnitRunStatus) error {
			return nil
		}
		iRunDB.Impl.Finish = func(context.Context, string) error {
			return nil
		}

		dataAgentRecord := domain.DataAgent{
			Name:         "fake-data-agent",
			Mode:         domain.DataAgentWrite,
			KnitDataBody: databody,
		}

		iDataDB := dbdatamock.NewDataInterface()
		iDataDB.Impl.NewAgent = func(_ context.Context, knitId string, mode domain.DataAgentMode, deadline time.Duration) (domain.DataAgent, error) {
			if mode != domain.DataAgentWrite {
				t.Errorf("DataAgentMode is not DataAgentWrite. actual = %s", mode)
			}
			if knitId != databody.KnitId {
				t.Errorf("KnitId is not expected. actual = %s, expected = %s", knitId, databody.KnitId)
			}
			return dataAgentRecord, nil
		}
		iDataDB.Impl.RemoveAgent = func(_ context.Context, name string) error {
			if name != dataAgentRecord.Name {
				t.Errorf("DataAgent.Name is not expected. actual = %s, expected = %s", name, dataAgentRecord.Name)
			}
			return nil
		}

		iDataK8s := k8sdatamocks.New(t)
		iDataK8s.Impl.SpawnDataAgent = func(context.Context, domain.DataAgent, time.Time) (dataagt.DataAgent, error) {
			return dagt, nil
		}
		testee := handlers.PostDataHandler(iDataDB, iRunDB, iDataK8s)

		e := echo.New()
		ectx, resprec := httptestutil.Post(e, "/api/backends/data/", bytes.NewBuffer([]byte("n/a")))
		ectx.SetPath("/api/backends/data/")

		if err := testee(ectx); err == nil {
			t.Errorf("PostDataHandler does not error. resp = %+v", resprec)
		} else if httperr := new(echo.HTTPError); !errors.As(err, &httperr) {
			t.Errorf("error is not echo.HTTPError. actual = %+v", err)
		} else if httperr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, httperr.Code)
		}

		if dagt.Calls.Close.Times() < 1 {
			t.Errorf("Close of dataagt has not been called")
		}

		if iRunDB.Calls.NewPseudo.Times() < 1 {
			// arguments are not interested; tested other testcases.
			t.Errorf("RunInterface.NewPseudo has not been called, but should")
		}

		{
			expected := []struct {
				RunId     string
				NewStatus domain.KnitRunStatus
			}{
				{RunId: runId, NewStatus: domain.Aborting},
			}
			actual := iRunDB.Calls.SetStatus
			if !cmp.SliceContentEq(expected, actual) {
				t.Errorf(
					"RunInterface.SetStatus\n===actual===\n%+v\n===expected===\n%+v",
					actual, expected,
				)
			}
		}
		{
			expected := []string{runId}
			actual := iRunDB.Calls.Finish
			if !cmp.SliceContentEq(expected, actual) {
				t.Errorf(
					"RunInterface.Finish\n===actual===\n%+v\n===expected===\n%+v",
					actual, expected,
				)
			}
		}

		if iDataDB.Calls.NewAgent.Times() < 1 {
			t.Errorf("DataInterface.NewAgent has not been called, but should")
		}
		if iDataDB.Calls.RemoveAgent.Times() < 1 {
			t.Errorf("DataInterface.RemoveAgent has not been called, but should")
		}

	})

	t.Run("when RunInterface.NewPseudo cause error, response 500", func(t *testing.T) {

		fakeError := errors.New("fake error")

		iDataDB := dbdatamock.NewDataInterface()

		iRunDB := dbrunmock.NewRunInterface()
		iRunDB.Impl.NewPseudo = func(_ context.Context, planName domain.PseudoPlanName, _ time.Duration) (string, error) {
			if planName != domain.Uploaded {
				t.Errorf(
					"RunInterface.NewPseudo\n===actual===\n%+v\n===expected===\n%+v",
					planName, domain.Uploaded,
				)
			}
			return "", fakeError
		}
		// other methods should not be called

		dagt := NewBrokenDataagt()

		iDataK8s := k8sdatamocks.New(t)
		iDataK8s.Impl.SpawnDataAgent = func(context.Context, domain.DataAgent, time.Time) (dataagt.DataAgent, error) {
			return dagt, nil
		}
		testee := handlers.PostDataHandler(iDataDB, iRunDB, iDataK8s)

		e := echo.New()
		ectx, resprec := httptestutil.Post(e, "/api/backends/data/", bytes.NewBuffer([]byte("n/a")))
		ectx.SetPath("/api/backends/data/")

		err := testee(ectx)
		if err == nil {
			t.Fatalf("PostDataHandler does not error. resp = %+v", resprec)
		}
		if httperr := new(echo.HTTPError); !errors.As(err, &httperr) {
			t.Errorf("error is not echo.HTTPError. actual = %+v", err)
		} else if httperr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, httperr.Code)
		}

		if 0 < dagt.Calls.Host.Times() {
			t.Errorf("dataagt should not be accessed")
		}
		if 0 < dagt.Calls.APIPort.Times() {
			t.Errorf("dataagt should not be accessed")
		}
		if 0 < dagt.Calls.Close.Times() {
			t.Errorf("dataagt should not be accessed")
		}

		{
			if iRunDB.Calls.NewPseudo.Times() < 1 {
				t.Errorf("RunInterface.NewPseudo has not been called, but should")
			}
		}
	})

	t.Run("when RunInterface.Get cause error, response 500", func(t *testing.T) {

		fakeError := errors.New("fake error")

		iDataDB := dbdatamock.NewDataInterface()

		runId := "run_id"
		iRunDB := dbrunmock.NewRunInterface()
		iRunDB.Impl.NewPseudo = func(_ context.Context, planName domain.PseudoPlanName, _ time.Duration) (string, error) {
			if planName != domain.Uploaded {
				t.Errorf(
					"RunInterface.NewPseudo\n===actual===\n%+v\n===expected===\n%+v",
					planName, domain.Uploaded,
				)
			}
			return runId, nil
		}
		iRunDB.Impl.Get = func(ctx context.Context, runId []string) (map[string]domain.Run, error) {
			return nil, fakeError
		}
		iRunDB.Impl.SetStatus = func(context.Context, string, domain.KnitRunStatus) error {
			return nil
		}
		iRunDB.Impl.Finish = func(context.Context, string) error {
			return nil
		}

		dagt := NewBrokenDataagt()

		iDataK8s := k8sdatamocks.New(t)
		iDataK8s.Impl.SpawnDataAgent = func(context.Context, domain.DataAgent, time.Time) (dataagt.DataAgent, error) {
			return dagt, nil
		}
		testee := handlers.PostDataHandler(iDataDB, iRunDB, iDataK8s)

		e := echo.New()
		ectx, resprec := httptestutil.Post(e, "/api/backends/data/", bytes.NewBuffer([]byte("n/a")))
		ectx.SetPath("/api/backends/data/")

		err := testee(ectx)
		if err == nil {
			t.Fatalf("PostDataHandler does not error. resp = %+v", resprec)
		}
		if httperr := new(echo.HTTPError); !errors.As(err, &httperr) {
			t.Errorf("error is not echo.HTTPError. actual = %+v", err)
		} else if httperr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, httperr.Code)
		}

		if 0 < dagt.Calls.Host.Times() {
			t.Errorf("dataagt should not be accessed")
		}
		if 0 < dagt.Calls.APIPort.Times() {
			t.Errorf("dataagt should not be accessed")
		}
		if 0 < dagt.Calls.Close.Times() {
			t.Errorf("dataagt should not be accessed")
		}

		{
			if iRunDB.Calls.NewPseudo.Times() < 1 {
				t.Errorf("RunInterface.NewPseudo has not been called, but should")
			}
		}
		{
			actual := iRunDB.Calls.Get
			expected := [][]string{{runId}}
			if !cmp.SliceContentEqWith(actual, expected, cmp.SliceContentEq[string]) {
				t.Errorf(
					"RunInterface.Get\n===actual===\n%+v\n===expected===\n%+v",
					actual, expected,
				)
			}
		}
		{
			actual := iRunDB.Calls.SetStatus
			expected := []struct {
				RunId     string
				NewStatus domain.KnitRunStatus
			}{
				{RunId: runId, NewStatus: domain.Aborting},
			}
			if !cmp.SliceContentEq(actual, expected) {
				t.Errorf(
					"RunInterface.SetStatus\n===actual===\n%+v\n===expected===\n%+v",
					actual, expected,
				)
			}
		}
		{
			actual := iRunDB.Calls.Finish
			expected := []string{runId}
			if !cmp.SliceContentEq(actual, expected) {
				t.Errorf(
					"RunInterface.Finish\n===actual===\n%+v\n===expected===\n%+v",
					actual, expected,
				)
			}
		}
	})

	t.Run("when RunInterface.SetStatus cause error, response 500", func(t *testing.T) {

		fakeError := errors.New("fake error")

		databody := domain.KnitDataBody{
			KnitId:    "knit-id",
			VolumeRef: "volume-ref",
		}

		runId := "run-id"
		iRunDB := dbrunmock.NewRunInterface()
		iRunDB.Impl.NewPseudo = func(_ context.Context, planName domain.PseudoPlanName, _ time.Duration) (string, error) {
			if planName != domain.Uploaded {
				t.Errorf(
					"RunInterface.NewPseudo\n===actual===\n%+v\n===expected===\n%+v",
					planName, domain.Uploaded,
				)
			}
			return runId, nil
		}
		iRunDB.Impl.Get = func(context.Context, []string) (map[string]domain.Run, error) {
			run := domain.Run{
				RunBody: domain.RunBody{
					Id: runId,
				},
				Outputs: []domain.Assignment{
					{
						KnitDataBody: databody,
						MountPoint:   domain.MountPoint{Id: 1},
					},
				},
			}
			return map[string]domain.Run{runId: run}, nil
		}
		iRunDB.Impl.SetStatus = func(context.Context, string, domain.KnitRunStatus) error {
			return fakeError
		}
		iRunDB.Impl.Finish = func(context.Context, string) error {
			return nil
		}
		// other methods should not be called

		dataagent := domain.DataAgent{
			Name:         "fake-data-agent",
			Mode:         domain.DataAgentWrite,
			KnitDataBody: databody,
		}
		iDataDB := dbdatamock.NewDataInterface()
		iDataDB.Impl.NewAgent = func(_ context.Context, knitId string, mode domain.DataAgentMode, _ time.Duration) (domain.DataAgent, error) {
			if mode != domain.DataAgentWrite {
				t.Errorf("DataAgentMode is not DataAgentWrite. actual = %s", mode)
			}
			if knitId != databody.KnitId {
				t.Errorf("KnitId is not expected. actual = %s, expected = %s", knitId, databody.KnitId)
			}
			return dataagent, nil
		}
		iDataDB.Impl.RemoveAgent = func(_ context.Context, name string) error {
			if name != dataagent.Name {
				t.Errorf("DataAgent.Name is not expected. actual = %s, expected = %s", name, dataagent.Name)
			}
			return nil
		}

		hdr := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
		})
		svr := httptest.NewServer(hdr)
		defer svr.Close()
		dagt := NewMockedDataagt(svr)
		dagt.Impl.KnitID = func() string { return databody.KnitId }
		dagt.Impl.Close = func() error { return nil }
		dagt.Impl.VolumeRef = func() string { return databody.VolumeRef }
		defer dagt.Close()

		iDataK8s := k8sdatamocks.New(t)
		iDataK8s.Impl.SpawnDataAgent = func(context.Context, domain.DataAgent, time.Time) (dataagt.DataAgent, error) {
			return dagt, nil
		}
		testee := handlers.PostDataHandler(iDataDB, iRunDB, iDataK8s)

		e := echo.New()
		ectx, resprec := httptestutil.Post(e, "/api/backends/data/", bytes.NewBuffer([]byte("n/a")))
		ectx.SetPath("/api/backends/data/")

		err := testee(ectx)
		if err == nil {
			t.Fatalf("PostDataHandler does not error. resp = %+v", resprec)
		}
		if httperr := new(echo.HTTPError); !errors.As(err, &httperr) {
			t.Errorf("error is not echo.HTTPError. actual = %+v", err)
		} else if httperr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, httperr.Code)
		}

		if dagt.Calls.Close.Times() < 1 {
			t.Errorf("dataagt is not closed")
		}

		if iRunDB.Calls.NewPseudo.Times() < 1 {
			t.Error("RunInterface.NewPseudo has not been called")
		}

		{
			actual := iRunDB.Calls.Get
			expected := [][]string{{runId}}
			if !cmp.SliceContentEqWith(actual, expected, cmp.SliceContentEq[string]) {
				t.Errorf(
					"RunInterface.Get\n===actual===\n%+v\n===expected===\n%+v",
					actual, expected,
				)
			}
		}
		{
			actual := iRunDB.Calls.SetStatus
			expected := []struct {
				RunId     string
				NewStatus domain.KnitRunStatus
			}{
				{RunId: runId, NewStatus: domain.Completing},
				{RunId: runId, NewStatus: domain.Aborting},
			}
			if !cmp.SliceContentEq(actual, expected) {
				t.Errorf(
					"RunInterface.SetStatus\n===actual===\n%+v\n===expected===\n%+v",
					actual, expected,
				)
			}
		}
		{
			actual := iRunDB.Calls.Finish
			expected := []string{runId}
			if !cmp.SliceContentEq(actual, expected) {
				t.Errorf(
					"RunInterface.Finish\n===actual===\n%+v\n===expected===\n%+v",
					actual, expected,
				)
			}
		}
	})

	t.Run("when RunInterface.Finish cause error, response 500", func(t *testing.T) {
		hdr := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte("example response"))
		})

		svr := httptest.NewServer(hdr)
		defer svr.Close()

		runId := "test-run-id"
		databody := domain.KnitDataBody{
			KnitId:    "test-knit-id",
			VolumeRef: "test-pvc-name",
		}

		dagt := NewMockedDataagt(svr)
		dagt.Impl.KnitID = func() string { return databody.KnitId }
		dagt.Impl.Close = func() error { return nil }
		dagt.Impl.VolumeRef = func() string { return databody.VolumeRef }

		iRunDB := dbrunmock.NewRunInterface()
		iRunDB.Impl.NewPseudo = func(context.Context, domain.PseudoPlanName, time.Duration) (string, error) {
			return runId, nil
		}
		iRunDB.Impl.Get = func(context.Context, []string) (map[string]domain.Run, error) {
			run := domain.Run{
				RunBody: domain.RunBody{
					Id: runId,
				},
				Outputs: []domain.Assignment{{KnitDataBody: databody}},
			}
			return map[string]domain.Run{runId: run}, nil
		}
		iRunDB.Impl.SetStatus = func(ctx context.Context, runId string, newStatus domain.KnitRunStatus) error {
			return nil
		}

		fakeError := errors.New("fake error")
		iRunDB.Impl.Finish = func(context.Context, string) error {
			return fakeError
		}

		dataagent := domain.DataAgent{
			Name:         "fake-data-agent",
			Mode:         domain.DataAgentWrite,
			KnitDataBody: databody,
		}
		iDataDB := dbdatamock.NewDataInterface()
		iDataDB.Impl.NewAgent = func(_ context.Context, knitId string, mode domain.DataAgentMode, _ time.Duration) (domain.DataAgent, error) {
			if mode != domain.DataAgentWrite {
				t.Errorf("DataAgentMode is not DataAgentWrite. actual = %s", mode)
			}
			if knitId != databody.KnitId {
				t.Errorf("KnitId is not expected. actual = %s, expected = %s", knitId, databody.KnitId)
			}
			return dataagent, nil
		}
		iDataDB.Impl.RemoveAgent = func(_ context.Context, name string) error {
			if name != dataagent.Name {
				t.Errorf("DataAgent.Name is not expected. actual = %s, expected = %s", name, dataagent.Name)
			}
			return nil
		}

		e := echo.New()
		payload := []byte("arbitary byte stream...")
		ectx, resprec := httptestutil.Post(
			e, "/api/backends/data/", bytes.NewBuffer(payload),
		)
		ectx.SetPath("/api/backends/data/")

		iDataK8s := k8sdatamocks.New(t)
		iDataK8s.Impl.SpawnDataAgent = func(context.Context, domain.DataAgent, time.Time) (dataagt.DataAgent, error) {
			return dagt, nil
		}
		testee := handlers.PostDataHandler(iDataDB, iRunDB, iDataK8s)

		err := testee(ectx)
		if err == nil {
			t.Fatalf("PostDataHandler does not cause error. response = %v", resprec)
		}
		if httperr := new(echo.HTTPError); !errors.As(err, &httperr) {
			t.Errorf("error is not echo.HTTPError. actual = %+v", err)
		} else if httperr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, httperr.Code)
		}
		if dagt.Calls.Close.Times() < 1 {
			t.Errorf("dataagt Calls should be called")
		}

		if iRunDB.Calls.NewPseudo.Times() < 1 {
			t.Errorf("DataInterface.Initialize has not been called.")
		}
		{
			actual := iRunDB.Calls.SetStatus
			expected := []struct {
				RunId     string
				NewStatus domain.KnitRunStatus
			}{
				{RunId: runId, NewStatus: domain.Completing},
				{RunId: runId, NewStatus: domain.Aborting},
			}
			if !cmp.SliceContentEq(actual, expected) {
				t.Errorf(
					"RunInterface.SetStatus\n===actual===\n%+v\n===expected===\n%+v",
					actual, expected,
				)
			}
		}
		{
			actual := iRunDB.Calls.Finish
			expected := []string{runId, runId}
			if !cmp.SliceContentEq(actual, expected) {
				t.Errorf(
					"RunInterface.Finish\n===actual===\n%+v\n===expected===\n%+v",
					actual, expected,
				)
			}
		}

	})

	t.Run("if request closed while sending data to Dataagt, it should be error totally.", func(t *testing.T) {
		// It is assuming cases such as
		// DataAgent pod is killed by kubectl or OOMKiller,
		// node is crushed during processing, etc.
		//
		// 1. request to backend api
		// 2. backend api proxies the request to the (virtual) dataagt, but closed suddenly from dataagt.
		// 3. backend api should handle such case as *failed* to make knit data.
		//

		runId := "test-run-id"
		databody := domain.KnitDataBody{
			KnitId: "test-knit-id", VolumeRef: "test-pvc-name",
		}

		iRunDB := dbrunmock.NewRunInterface()
		iRunDB.Impl.NewPseudo = func(context.Context, domain.PseudoPlanName, time.Duration) (string, error) {
			return runId, nil
		}
		iRunDB.Impl.Get = func(context.Context, []string) (map[string]domain.Run, error) {
			run := domain.Run{
				RunBody: domain.RunBody{
					Id: runId,
				},
				Outputs: []domain.Assignment{{KnitDataBody: databody}},
			}
			return map[string]domain.Run{runId: run}, nil
		}
		iRunDB.Impl.SetStatus = func(context.Context, string, domain.KnitRunStatus) error {
			return nil
		}
		iRunDB.Impl.Finish = func(context.Context, string) error { return nil }

		iDataDB := dbdatamock.NewDataInterface()
		dataagent := domain.DataAgent{
			Name:         "fake-data-agent",
			Mode:         domain.DataAgentWrite,
			KnitDataBody: databody,
		}
		iDataDB.Impl.NewAgent = func(_ context.Context, knitId string, mode domain.DataAgentMode, _ time.Duration) (domain.DataAgent, error) {
			if mode != domain.DataAgentWrite {
				t.Errorf("DataAgentMode is not DataAgentWrite. actual = %s", mode)
			}
			if knitId != databody.KnitId {
				t.Errorf("KnitId is not expected. actual = %s, expected = %s", knitId, databody.KnitId)
			}
			return dataagent, nil
		}
		iDataDB.Impl.RemoveAgent = func(_ context.Context, name string) error {
			if name != "fake-data-agent" {
				t.Errorf("DataAgent.Name is not expected. actual = %s, expected = %s", name, "fake-data-agent")
			}
			return nil
		}
		e := echo.New()

		pr, pw := io.Pipe()
		defer pr.Close()
		go func() {
			defer pw.Close()
			pw.Write([]byte("msg1,"))
			pw.Write([]byte("msg2,"))
			pw.Write([]byte("message3"))
		}()
		var svr *httptest.Server
		svr = httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Body.Close()
				svr.CloseClientConnections()
			}),
		)
		defer svr.Close()
		dagt := NewMockedDataagt(svr)
		dagt.Impl.KnitID = func() string { return databody.KnitId }
		dagt.Impl.Close = func() error { return nil }
		dagt.Impl.VolumeRef = func() string { return databody.VolumeRef }

		iDataK8s := k8sdatamocks.New(t)
		iDataK8s.Impl.SpawnDataAgent = func(context.Context, domain.DataAgent, time.Time) (dataagt.DataAgent, error) {
			return dagt, nil
		}
		testee := handlers.PostDataHandler(iDataDB, iRunDB, iDataK8s)

		ectx, _ := httptestutil.Post(
			e, "/api/backends/data/", pr,
			httptestutil.ContentType("example/test-data"),
			httptestutil.Chunked(),
			httptestutil.WithHeader("x-custom-header", "header-value", "header-value-2"),
			httptestutil.WithTrailer("x-usersending-trailer", "trailer-value", "trailer-value-2"),
		)
		ectx.SetPath("/api/backends/data/")

		err := testee(ectx)

		if err == nil {
			t.Fatalf("PostDataHander does not cause error when request is closed in the middle.")
		}
		if httperr := new(echo.HTTPError); !errors.As(err, &httperr) {
			t.Errorf("error is not echo.HTTPError. actual = %+v", err)
		} else if httperr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, httperr.Code)
		}
		if dagt.Calls.Close.Times() < 1 {
			t.Errorf("PostDataHandler should be closed")
		}
		{
			actual := iRunDB.Calls.SetStatus
			expected := []struct {
				RunId     string
				NewStatus domain.KnitRunStatus
			}{
				{RunId: runId, NewStatus: domain.Aborting},
			}
			if !cmp.SliceContentEq(actual, expected) {
				t.Errorf(
					"RunInterface.SetStatus\n===actual===\n%+v\n===expected===\n%+v",
					actual, expected,
				)
			}
		}
		{
			actual := iRunDB.Calls.Finish
			expected := []string{runId}
			if !cmp.SliceContentEq(actual, expected) {
				t.Errorf(
					"RunInterface.Finish\n===actual===\n%+v\n===expected===\n%+v",
					actual, expected,
				)
			}
		}
	})

}

func TestImportDataBeginHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		k := try.To(key.HS256(3*time.Hour, 2048/8).Issue()).OrFatal(t)
		kp := mockkeyprovider.New(t)
		kp.Impl.Provide = func(ctx context.Context, req ...keychain.KeyRequirement) (string, key.Key, error) {
			return "test-key", k, nil
		}

		run := domain.Run{
			RunBody: domain.RunBody{
				Id:     "run-id",
				Status: domain.Running,
				PlanBody: domain.PlanBody{
					PlanId: "test-plan-id",
					Pseudo: &domain.PseudoPlanDetail{Name: domain.Imported},
				},
			},
			Outputs: []domain.Assignment{
				{
					KnitDataBody: domain.KnitDataBody{
						KnitId:    "test-knit-id",
						VolumeRef: "test-volume-ref",
					},
				},
			},
		}

		irun := dbrunmock.NewRunInterface()
		irun.Impl.NewPseudo = func(ctx context.Context, planName domain.PseudoPlanName, d time.Duration) (string, error) {
			if planName != domain.Imported {
				t.Errorf("NewPseudo should be called with Imported. actual = %s", planName)
			}
			return run.Id, nil
		}
		irun.Impl.Get = func(context.Context, []string) (map[string]domain.Run, error) {
			if !cmp.SliceContentEq([]string{"run-id"}, []string{run.Id}) {
				t.Errorf("Get should be called with {run-id}. actual = %s", run.Id)
			}
			return map[string]domain.Run{run.Id: run}, nil
		}
		testee := handlers.ImportDataBeginHandler(kp, irun)

		e := echo.New()
		ectx, resprec := httptestutil.Post(e, "/api/backends/data/import/begin", nil)
		ectx.SetPath("/api/backends/data/import/begin")

		if err := testee(ectx); err != nil {
			t.Fatalf("ImportDataBeginHandler does not cause error. resp = %+v", resprec)
		}

		if got := resprec.Result().StatusCode; got != http.StatusOK {
			t.Errorf("status code is not 200. actual = %d", got)
		}

		if got := resprec.Header().Get("Content-Type"); got != "application/jwt" {
			t.Errorf("Content-Type is not application/jwt. actual = %s", got)
		}

		body := resprec.Body.String()

		claim := try.To(jwt.ParseWithClaims(
			body,
			&handlers.DataImportClaim{},
			func(t *jwt.Token) (interface{}, error) { return k.ToVerify(), nil },
		)).OrFatal(t)

		{
			header := claim.Header
			if got := header["kid"]; got != "test-key" {
				t.Errorf("kid is not expected. actual = %s", got)
			}

			if got := header["alg"]; got != k.Alg() {
				t.Errorf("alg is not expected. actual = %s", got)
			}
		}

		if c, ok := claim.Claims.(*handlers.DataImportClaim); !ok {
			t.Fatalf("claim is not DataImportClaim. actual = %T", claim.Claims)
		} else {
			if c.ID == "" {
				t.Error("ID is empty")
			}
			if c.RunId != run.Id {
				t.Errorf("RunId is not expected. actual = %s, expected = %s", c.RunId, run.Id)
			}
			if c.KnitId != run.Outputs[0].KnitDataBody.KnitId {
				t.Errorf("KnitId is not expected. actual = %s, expected = %s", c.KnitId, run.Outputs[0].KnitDataBody.KnitId)
			}
			if c.Subject != run.Outputs[0].KnitDataBody.VolumeRef {
				t.Errorf("VolumeRef is not expected. actual = %s, expected = %s", c.Subject, run.Outputs[0].KnitDataBody.VolumeRef)
			}
		}
	})

	t.Run("when KeyProvider.Provide cause error, response 500", func(t *testing.T) {
		kp := mockkeyprovider.New(t)
		fakeError := errors.New("fake error")
		kp.Impl.Provide = func(context.Context, ...keychain.KeyRequirement) (string, key.Key, error) {
			return "", nil, fakeError
		}

		run := domain.Run{
			RunBody: domain.RunBody{
				Id:     "run-id",
				Status: domain.Running,
				PlanBody: domain.PlanBody{
					PlanId: "test-plan-id",
					Pseudo: &domain.PseudoPlanDetail{Name: domain.Imported},
				},
			},
			Outputs: []domain.Assignment{
				{
					KnitDataBody: domain.KnitDataBody{
						KnitId:    "test-knit-id",
						VolumeRef: "test-volume-ref",
					},
				},
			},
		}
		irun := dbrunmock.NewRunInterface()
		irun.Impl.NewPseudo = func(context.Context, domain.PseudoPlanName, time.Duration) (string, error) {
			return run.Id, nil
		}
		irun.Impl.Get = func(context.Context, []string) (map[string]domain.Run, error) {
			if !cmp.SliceContentEq([]string{"run-id"}, []string{run.Id}) {
				t.Errorf("Get should be called with {run-id}. actual = %s", run.Id)
			}
			return map[string]domain.Run{run.Id: run}, nil
		}

		testee := handlers.ImportDataBeginHandler(kp, irun)

		e := echo.New()
		ectx, _ := httptestutil.Post(e, "/api/backends/data/import/begin", nil)
		ectx.SetPath("/api/backends/data/import/begin")

		err := testee(ectx)
		if !errors.Is(err, fakeError) {
			t.Fatalf("ImportDataBeginHandler does not cause unexpected error: %+v", err)
		}

		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %+v", err)
		} else if herr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, herr.Code)
		}
	})

	t.Run("when RunInterface.NewPseudo returns non single output Run, response 500", func(t *testing.T) {
		kp := mockkeyprovider.New(t)

		run := domain.Run{
			RunBody: domain.RunBody{
				Id:     "run-id",
				Status: domain.Running,
				PlanBody: domain.PlanBody{
					PlanId: "test-plan-id",
					Pseudo: &domain.PseudoPlanDetail{Name: domain.Imported},
				},
			},
			Outputs: []domain.Assignment{
				{
					KnitDataBody: domain.KnitDataBody{
						KnitId:    "test-knit-id",
						VolumeRef: "test-volume-ref",
					},
				},
				{
					KnitDataBody: domain.KnitDataBody{
						KnitId:    "test-knit-id-2",
						VolumeRef: "test-volume-ref-2",
					},
				},
			},
		}
		irun := dbrunmock.NewRunInterface()
		irun.Impl.NewPseudo = func(context.Context, domain.PseudoPlanName, time.Duration) (string, error) {
			return run.Id, nil
		}
		irun.Impl.Get = func(context.Context, []string) (map[string]domain.Run, error) {
			if !cmp.SliceContentEq([]string{"run-id"}, []string{run.Id}) {
				t.Errorf("Get should be called with {run-id}. actual = %s", run.Id)
			}
			return map[string]domain.Run{run.Id: run}, nil
		}

		testee := handlers.ImportDataBeginHandler(kp, irun)

		e := echo.New()
		ectx, _ := httptestutil.Post(e, "/api/backends/data/import/begin", nil)
		ectx.SetPath("/api/backends/data/import/begin")

		err := testee(ectx)
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %+v", err)
		} else if herr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, herr.Code)
		}
	})

	t.Run("when RunInterface.Get cause error, response 500", func(t *testing.T) {
		kp := mockkeyprovider.New(t)
		irun := dbrunmock.NewRunInterface()
		irun.Impl.NewPseudo = func(context.Context, domain.PseudoPlanName, time.Duration) (string, error) {
			return "run-id", nil
		}

		fakeError := errors.New("fake error")
		irun.Impl.Get = func(context.Context, []string) (map[string]domain.Run, error) {
			return nil, fakeError
		}

		testee := handlers.ImportDataBeginHandler(kp, irun)

		e := echo.New()
		ectx, _ := httptestutil.Post(e, "/api/backends/data/import/begin", nil)
		ectx.SetPath("/api/backends/data/import/begin")

		err := testee(ectx)
		if !errors.Is(err, fakeError) {
			t.Fatalf("ImportDataBeginHandler does not cause unexpected error: %+v", err)
		}
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %+v", err)
		} else if herr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, herr.Code)
		}
	})

	t.Run("when RunInterface.Get does not return a map contains the new Run, response 500", func(t *testing.T) {
		kp := mockkeyprovider.New(t)
		irun := dbrunmock.NewRunInterface()
		irun.Impl.NewPseudo = func(context.Context, domain.PseudoPlanName, time.Duration) (string, error) {
			return "run-id", nil
		}
		irun.Impl.Get = func(context.Context, []string) (map[string]domain.Run, error) {
			return map[string]domain.Run{}, nil
		}

		testee := handlers.ImportDataBeginHandler(kp, irun)

		e := echo.New()
		ectx, _ := httptestutil.Post(e, "/api/backends/data/import/begin", nil)
		ectx.SetPath("/api/backends/data/import/begin")

		err := testee(ectx)
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %+v", err)
		} else if herr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, herr.Code)
		}
	})

	t.Run("when RunInterface.NewPseudo cause error, response 500", func(t *testing.T) {
		kp := mockkeyprovider.New(t)
		fakeError := errors.New("fake error")
		irun := dbrunmock.NewRunInterface()
		irun.Impl.NewPseudo = func(context.Context, domain.PseudoPlanName, time.Duration) (string, error) {
			return "", fakeError
		}

		testee := handlers.ImportDataBeginHandler(kp, irun)

		e := echo.New()
		ectx, _ := httptestutil.Post(e, "/api/backends/data/import/begin", nil)
		ectx.SetPath("/api/backends/data/import/begin")

		err := testee(ectx)
		if !errors.Is(err, fakeError) {
			t.Fatalf("ImportDataBeginHandler does not cause unexpected error: %+v", err)
		}
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %+v", err)
		} else if herr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, herr.Code)
		}
	})
}

func TestImpoerDataEndHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		claim := &handlers.DataImportClaim{
			RegisteredClaims: jwt.RegisteredClaims{
				ID:      "nonce",
				Subject: "test-volume-ref",
			},
			RunId:  "test-run-id",
			KnitId: "test-knit-id",
		}

		k := try.To(key.HS256(3*time.Hour, 2048/8).Issue()).OrFatal(t)

		token := try.To(keychain.NewJWS("test-key-id", k, claim)).OrFatal(t)

		kp := mockkeyprovider.New(t)
		kp.Impl.GetKeychain = func(context.Context) (keychain.Keychain, error) {
			mkc := mockkeychain.New(t)
			mkc.Impl.GetKey = func(options ...keychain.KeyRequirement) (string, key.Key, bool) {
				return "test-key", k, true
			}
			return mkc, nil
		}

		dbRun := dbrunmock.NewRunInterface()
		dbRun.Impl.SetStatus = func(ctx context.Context, runId string, newStatus domain.KnitRunStatus) error {
			if runId != claim.RunId {
				t.Errorf("RunId is not expected. actual = %s, expected = %s", runId, claim.RunId)
			}
			if newStatus != domain.Completing {
				t.Errorf("NewStatus is not expected. actual = %s, expected = %s", newStatus, domain.Completing)
			}
			return nil
		}

		data := domain.KnitData{
			KnitDataBody: domain.KnitDataBody{
				KnitId:    claim.KnitId,
				VolumeRef: claim.Subject,
			},
			Upsteram: domain.DataSource{
				MountPoint: &domain.MountPoint{Id: 1, Path: "/imported"},
				RunBody: domain.RunBody{
					Id: claim.RunId, Status: domain.Completing,
					PlanBody: domain.PlanBody{
						PlanId: "test-plan-id",
						Pseudo: &domain.PseudoPlanDetail{Name: domain.Imported},
					},
				},
			},
		}

		dbData := dbdatamock.NewDataInterface()
		dbData.Impl.Get = func(ctx context.Context, knitId []string) (map[string]domain.KnitData, error) {
			if !cmp.SliceContentEq([]string{claim.KnitId}, knitId) {
				t.Errorf("Get should be called with {%s}. actual = %s", claim.KnitId, knitId)
			}
			return map[string]domain.KnitData{claim.KnitId: data}, nil
		}

		k8sData := k8sdatamocks.New(t)
		k8sData.Impl.ChechDataisBound = func(ctx context.Context, da domain.KnitDataBody) (bool, error) {
			return true, nil
		}

		testee := handlers.ImportDataEndHandler(k8sData, kp, dbRun, dbData)
		e := echo.New()
		ectx, resprec := httptestutil.Post(
			e, "/api/backends/data/import/end", bytes.NewBufferString(token),
			httptestutil.ContentType("application/jwt"),
		)
		ectx.SetPath("/api/backends/data/import/end")

		if err := testee(ectx); err != nil {
			t.Fatalf("ImportDataEndHandler should not cause error:%s", err)
		}

		if got := resprec.Result().StatusCode; got != http.StatusOK {
			t.Errorf("status code is not 200. actual = %d", got)
		}

		if got := resprec.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type is not application/json. actual = %s", got)
		}

		parsed := new(apidata.Detail)
		if err := json.NewDecoder(resprec.Body).Decode(parsed); err != nil {
			t.Fatalf("response body is not JSON: %s", err)
		}
		want := binddata.ComposeDetail(data)
		if !want.Equal(*parsed) {
			t.Errorf("response body is not expected. actual = %+v, expected = %+v", parsed, want)
		}
	})

	for name, testcase := range map[string]struct {
		expecterErr error
		statusCode  int
	}{
		"ErrInvalidRunStateChanging": {
			expecterErr: domain.ErrInvalidRunStateChanging,
			statusCode:  http.StatusConflict,
		},
		"ErrMissing": {
			expecterErr: kerr.ErrMissing,
			statusCode:  http.StatusConflict,
		},
		"unexpected one": {
			expecterErr: errors.New("unexpected error"),
			statusCode:  http.StatusInternalServerError,
		},
	} {
		t.Run(fmt.Sprintf("when dbRun.SetStatus cause error %s, it responses 409", name), func(t *testing.T) {
			claim := &handlers.DataImportClaim{
				RegisteredClaims: jwt.RegisteredClaims{
					ID:      "nonce",
					Subject: "test-volume-ref",
				},
				RunId:  "test-run-id",
				KnitId: "test-knit-id",
			}

			k := try.To(key.HS256(3*time.Hour, 2048/8).Issue()).OrFatal(t)

			token := try.To(keychain.NewJWS("test-key-id", k, claim)).OrFatal(t)

			kp := mockkeyprovider.New(t)
			kp.Impl.GetKeychain = func(context.Context) (keychain.Keychain, error) {
				mkc := mockkeychain.New(t)
				mkc.Impl.GetKey = func(options ...keychain.KeyRequirement) (string, key.Key, bool) {
					return "test-key", k, true
				}
				return mkc, nil
			}

			dbRun := dbrunmock.NewRunInterface()
			dbRun.Impl.SetStatus = func(ctx context.Context, runId string, newStatus domain.KnitRunStatus) error {
				if runId != claim.RunId {
					t.Errorf("RunId is not expected. actual = %s, expected = %s", runId, claim.RunId)
				}
				return testcase.expecterErr
			}

			data := domain.KnitData{
				KnitDataBody: domain.KnitDataBody{
					KnitId:    claim.KnitId,
					VolumeRef: claim.Subject,
				},
				Upsteram: domain.DataSource{
					MountPoint: &domain.MountPoint{Id: 1, Path: "/imported"},
					RunBody: domain.RunBody{
						Id: claim.RunId, Status: domain.Completing,
						PlanBody: domain.PlanBody{
							PlanId: "test-plan-id",
							Pseudo: &domain.PseudoPlanDetail{Name: domain.Imported},
						},
					},
				},
			}
			dbData := dbdatamock.NewDataInterface()
			dbData.Impl.Get = func(ctx context.Context, knitId []string) (map[string]domain.KnitData, error) {
				if !cmp.SliceContentEq([]string{claim.KnitId}, knitId) {
					t.Errorf("Get should be called with {%s}. actual = %s", claim.KnitId, knitId)
				}
				return map[string]domain.KnitData{claim.KnitId: data}, nil
			}

			k8sData := k8sdatamocks.New(t)
			k8sData.Impl.ChechDataisBound = func(ctx context.Context, da domain.KnitDataBody) (bool, error) {
				return true, nil
			}

			testee := handlers.ImportDataEndHandler(k8sData, kp, dbRun, dbData)
			e := echo.New()
			ectx, _ := httptestutil.Post(
				e, "/api/backends/data/import/end", bytes.NewBufferString(token),
				httptestutil.ContentType("application/jwt"),
			)
			ectx.SetPath("/api/backends/data/import/end")

			err := testee(ectx)
			if !errors.Is(err, testcase.expecterErr) {
				t.Fatalf("ImportDataEndHandler does not cause unexpected error: %s", err)
			}
			if herr := new(echo.HTTPError); !errors.As(err, &herr) {
				t.Fatalf("error is not echo.HTTPError: %s", err)
			} else if herr.Code != testcase.statusCode {
				t.Errorf("error code is not %d. actual = %d", testcase.statusCode, herr.Code)
			}
		})
	}

	t.Run("when PVC is not bound, it responses 400", func(t *testing.T) {
		claim := &handlers.DataImportClaim{
			RegisteredClaims: jwt.RegisteredClaims{
				ID:      "nonce",
				Subject: "test-volume-ref",
			},
			RunId:  "test-run-id",
			KnitId: "test-knit-id",
		}

		k := try.To(key.HS256(3*time.Hour, 2048/8).Issue()).OrFatal(t)

		token := try.To(keychain.NewJWS("test-key-id", k, claim)).OrFatal(t)

		kp := mockkeyprovider.New(t)
		kp.Impl.GetKeychain = func(context.Context) (keychain.Keychain, error) {
			mkc := mockkeychain.New(t)
			mkc.Impl.GetKey = func(options ...keychain.KeyRequirement) (string, key.Key, bool) {
				return "test-key", k, true
			}
			return mkc, nil
		}

		dbRun := dbrunmock.NewRunInterface()
		dbData := dbdatamock.NewDataInterface()
		dbData.Impl.Get = func(ctx context.Context, knitId []string) (map[string]domain.KnitData, error) {
			if !cmp.SliceContentEq([]string{claim.KnitId}, knitId) {
				t.Errorf("Get should be called with {%s}. actual = %s", claim.KnitId, knitId)
			}
			return map[string]domain.KnitData{claim.KnitId: {}}, nil
		}

		k8sData := k8sdatamocks.New(t)
		k8sData.Impl.ChechDataisBound = func(ctx context.Context, da domain.KnitDataBody) (bool, error) {
			return false, nil
		}

		testee := handlers.ImportDataEndHandler(k8sData, kp, dbRun, dbData)
		e := echo.New()
		ectx, _ := httptestutil.Post(
			e, "/api/backends/data/import/end", bytes.NewBufferString(token),
			httptestutil.WithHeader("Content-Type", "application/jwt"),
		)
		ectx.SetPath("/api/backends/data/import/end")

		err := testee(ectx)
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %s", err)
		} else if herr.Code != http.StatusBadRequest {
			t.Errorf("error code is not %d. actual = %d", http.StatusBadRequest, herr.Code)
		}
	})

	t.Run("when PVC is missing, it responses 400", func(t *testing.T) {
		claim := &handlers.DataImportClaim{
			RegisteredClaims: jwt.RegisteredClaims{
				ID:      "nonce",
				Subject: "test-volume-ref",
			},
			RunId:  "test-run-id",
			KnitId: "test-knit-id",
		}

		k := try.To(key.HS256(3*time.Hour, 2048/8).Issue()).OrFatal(t)

		token := try.To(keychain.NewJWS("test-key-id", k, claim)).OrFatal(t)

		kp := mockkeyprovider.New(t)
		kp.Impl.GetKeychain = func(context.Context) (keychain.Keychain, error) {
			mkc := mockkeychain.New(t)
			mkc.Impl.GetKey = func(options ...keychain.KeyRequirement) (string, key.Key, bool) {
				return "test-key", k, true
			}
			return mkc, nil
		}

		dbRun := dbrunmock.NewRunInterface()
		dbData := dbdatamock.NewDataInterface()
		dbData.Impl.Get = func(ctx context.Context, knitId []string) (map[string]domain.KnitData, error) {
			if !cmp.SliceContentEq([]string{claim.KnitId}, knitId) {
				t.Errorf("Get should be called with {%s}. actual = %s", claim.KnitId, knitId)
			}
			return map[string]domain.KnitData{claim.KnitId: {}}, nil
		}

		k8sData := k8sdatamocks.New(t)
		k8sData.Impl.ChechDataisBound = func(ctx context.Context, da domain.KnitDataBody) (bool, error) {
			return false, &k8serrors.ErrMissing{}
		}

		testee := handlers.ImportDataEndHandler(k8sData, kp, dbRun, dbData)
		e := echo.New()
		ectx, _ := httptestutil.Post(
			e, "/api/backends/data/import/end", bytes.NewBufferString(token),
			httptestutil.WithHeader("Content-Type", "application/jwt"),
		)
		ectx.SetPath("/api/backends/data/import/end")

		err := testee(ectx)
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %s", err)
		} else if herr.Code != http.StatusBadRequest {
			t.Errorf("error code is not %d. actual = %d", http.StatusBadRequest, herr.Code)
		}
	})

	t.Run("when GetPVC cause error, it responses 500", func(t *testing.T) {
		claim := &handlers.DataImportClaim{
			RegisteredClaims: jwt.RegisteredClaims{
				ID:      "nonce",
				Subject: "test-volume-ref",
			},
			RunId:  "test-run-id",
			KnitId: "test-knit-id",
		}

		k := try.To(key.HS256(3*time.Hour, 2048/8).Issue()).OrFatal(t)

		token := try.To(keychain.NewJWS("test-key-id", k, claim)).OrFatal(t)

		kp := mockkeyprovider.New(t)
		kp.Impl.GetKeychain = func(context.Context) (keychain.Keychain, error) {
			mkc := mockkeychain.New(t)
			mkc.Impl.GetKey = func(options ...keychain.KeyRequirement) (string, key.Key, bool) {
				return "test-key", k, true
			}
			return mkc, nil
		}

		dbRun := dbrunmock.NewRunInterface()
		dbData := dbdatamock.NewDataInterface()
		dbData.Impl.Get = func(ctx context.Context, knitId []string) (map[string]domain.KnitData, error) {
			if !cmp.SliceContentEq([]string{claim.KnitId}, knitId) {
				t.Errorf("Get should be called with {%s}. actual = %s", claim.KnitId, knitId)
			}
			return map[string]domain.KnitData{claim.KnitId: {}}, nil
		}

		expectedError := errors.New("fake error")
		k8sData := k8sdatamocks.New(t)
		k8sData.Impl.ChechDataisBound = func(ctx context.Context, da domain.KnitDataBody) (bool, error) {
			return false, expectedError
		}

		testee := handlers.ImportDataEndHandler(k8sData, kp, dbRun, dbData)
		e := echo.New()
		ectx, _ := httptestutil.Post(
			e, "/api/backends/data/import/end", bytes.NewBufferString(token),
			httptestutil.WithHeader("Content-Type", "application/jwt"),
		)
		ectx.SetPath("/api/backends/data/import/end")

		err := testee(ectx)
		if !errors.Is(err, expectedError) {
			t.Fatalf("ImportDataEndHandler does not cause unexpected error: %s", err)
		}
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %s", err)
		} else if herr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, herr.Code)
		}
	})

	t.Run("when dbData.Get does not return Data in token, it responses 500", func(t *testing.T) {
		claim := &handlers.DataImportClaim{
			RegisteredClaims: jwt.RegisteredClaims{
				ID:      "nonce",
				Subject: "test-volume-ref",
			},
			RunId:  "test-run-id",
			KnitId: "test-knit-id",
		}

		k := try.To(key.HS256(3*time.Hour, 2048/8).Issue()).OrFatal(t)

		token := try.To(keychain.NewJWS("test-key-id", k, claim)).OrFatal(t)

		kp := mockkeyprovider.New(t)
		kp.Impl.GetKeychain = func(context.Context) (keychain.Keychain, error) {
			mkc := mockkeychain.New(t)
			mkc.Impl.GetKey = func(options ...keychain.KeyRequirement) (string, key.Key, bool) {
				return "test-key", k, true
			}
			return mkc, nil
		}

		dbRun := dbrunmock.NewRunInterface()

		dbData := dbdatamock.NewDataInterface()
		dbData.Impl.Get = func(ctx context.Context, knitId []string) (map[string]domain.KnitData, error) {
			if !cmp.SliceContentEq([]string{claim.KnitId}, knitId) {
				t.Errorf("Get should be called with {%s}. actual = %s", claim.KnitId, knitId)
			}
			return map[string]domain.KnitData{}, nil
		}

		k8sData := k8sdatamocks.New(t)

		testee := handlers.ImportDataEndHandler(k8sData, kp, dbRun, dbData)
		e := echo.New()
		ectx, _ := httptestutil.Post(
			e, "/api/backends/data/import/end", bytes.NewBufferString(token),
			httptestutil.WithHeader("Content-Type", "application/jwt"),
		)
		ectx.SetPath("/api/backends/data/import/end")

		err := testee(ectx)
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %s", err)
		} else if herr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, herr.Code)
		}
	})

	t.Run("when dbData.Get cause error, it responses 500", func(t *testing.T) {
		claim := &handlers.DataImportClaim{
			RegisteredClaims: jwt.RegisteredClaims{
				ID:      "nonce",
				Subject: "test-volume-ref",
			},
			RunId:  "test-run-id",
			KnitId: "test-knit-id",
		}

		k := try.To(key.HS256(3*time.Hour, 2048/8).Issue()).OrFatal(t)

		token := try.To(keychain.NewJWS("test-key-id", k, claim)).OrFatal(t)

		kp := mockkeyprovider.New(t)
		kp.Impl.GetKeychain = func(context.Context) (keychain.Keychain, error) {
			mkc := mockkeychain.New(t)
			mkc.Impl.GetKey = func(options ...keychain.KeyRequirement) (string, key.Key, bool) {
				return "test-key", k, true
			}
			return mkc, nil
		}

		dbRun := dbrunmock.NewRunInterface()

		fakeError := errors.New("fake error")
		dbData := dbdatamock.NewDataInterface()
		dbData.Impl.Get = func(ctx context.Context, knitId []string) (map[string]domain.KnitData, error) {
			if !cmp.SliceContentEq([]string{claim.KnitId}, knitId) {
				t.Errorf("Get should be called with {%s}. actual = %s", claim.KnitId, knitId)
			}
			return nil, fakeError
		}

		k8sData := k8sdatamocks.New(t)

		testee := handlers.ImportDataEndHandler(k8sData, kp, dbRun, dbData)
		e := echo.New()
		ectx, _ := httptestutil.Post(
			e, "/api/backends/data/import/end", bytes.NewBufferString(token),
			httptestutil.ContentType("application/jwt"),
		)
		ectx.SetPath("/api/backends/data/import/end")

		err := testee(ectx)
		if !errors.Is(err, fakeError) {
			t.Fatalf("ImportDataEndHandler does not cause unexpected error: %s", err)
		}
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %s", err)
		} else if herr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, herr.Code)
		}
	})

	t.Run("when token is invalid, it responses 400", func(t *testing.T) {
		claim := &handlers.DataImportClaim{
			RegisteredClaims: jwt.RegisteredClaims{
				ID:      "nonce",
				Subject: "test-volume-ref",
			},
			RunId:  "test-run-id",
			KnitId: "test-knit-id",
		}

		k := try.To(key.HS256(3*time.Hour, 2048/8).Issue()).OrFatal(t)

		token := try.To(keychain.NewJWS("test-key-id", k, claim)).OrFatal(t)

		kp := mockkeyprovider.New(t)
		kp.Impl.GetKeychain = func(context.Context) (keychain.Keychain, error) {
			kc := mockkeychain.New(t)
			kc.Impl.GetKey = func(options ...keychain.KeyRequirement) (string, key.Key, bool) {
				wrongKey := try.To(key.HS256(3*time.Hour, 2048/8).Issue()).OrFatal(t)
				return "test-key-id", wrongKey, true
			}
			return kc, nil
		}

		dbRun := dbrunmock.NewRunInterface()
		dbData := dbdatamock.NewDataInterface()

		k8sData := k8sdatamocks.New(t)

		testee := handlers.ImportDataEndHandler(k8sData, kp, dbRun, dbData)
		e := echo.New()
		ectx, _ := httptestutil.Post(
			e, "/api/backends/data/import/end", bytes.NewBufferString(token),
			httptestutil.ContentType("application/jwt"),
		)
		ectx.SetPath("/api/backends/data/import/end")

		err := testee(ectx)
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %s", err)
		} else if herr.Code != http.StatusUnauthorized {
			t.Errorf("error code is not %d. actual = %d", http.StatusUnauthorized, herr.Code)
		}
	})

	t.Run("when key provider's GetKeychain cause error, it responses 500", func(t *testing.T) {
		claim := &handlers.DataImportClaim{
			RegisteredClaims: jwt.RegisteredClaims{
				ID:      "nonce",
				Subject: "test-volume-ref",
			},
			RunId:  "test-run-id",
			KnitId: "test-knit-id",
		}

		k := try.To(key.HS256(3*time.Hour, 2048/8).Issue()).OrFatal(t)

		token := try.To(keychain.NewJWS("test-key-id", k, claim)).OrFatal(t)

		kp := mockkeyprovider.New(t)
		kp.Impl.GetKeychain = func(context.Context) (keychain.Keychain, error) {
			return nil, errors.New("fake error")
		}

		dbRun := dbrunmock.NewRunInterface()
		dbData := dbdatamock.NewDataInterface()

		k8sData := k8sdatamocks.New(t)

		testee := handlers.ImportDataEndHandler(k8sData, kp, dbRun, dbData)
		e := echo.New()
		ectx, _ := httptestutil.Post(
			e, "/api/backends/data/import/end", bytes.NewBufferString(token),
			httptestutil.ContentType("application/jwt"),
		)
		ectx.SetPath("/api/backends/data/import/end")

		err := testee(ectx)
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %s", err)
		} else if herr.Code != http.StatusInternalServerError {
			t.Errorf("error code is not %d. actual = %d", http.StatusInternalServerError, herr.Code)
		}
	})

	t.Run("when no content body, it responses 400", func(t *testing.T) {
		kp := mockkeyprovider.New(t)
		dbRun := dbrunmock.NewRunInterface()
		dbData := dbdatamock.NewDataInterface()
		k8sData := k8sdatamocks.New(t)

		testee := handlers.ImportDataEndHandler(k8sData, kp, dbRun, dbData)
		e := echo.New()
		ectx, _ := httptestutil.Post(e, "/api/backends/data/import/end", nil)
		ectx.SetPath("/api/backends/data/import/end")

		err := testee(ectx)
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %s", err)
		} else if herr.Code != http.StatusBadRequest {
			t.Errorf("error code is not %d. actual = %d", http.StatusBadRequest, herr.Code)
		}
	})

	t.Run("when content type is not application/jwt, it responses 400", func(t *testing.T) {
		kp := mockkeyprovider.New(t)
		dbRun := dbrunmock.NewRunInterface()
		dbData := dbdatamock.NewDataInterface()

		k8sData := k8sdatamocks.New(t)

		testee := handlers.ImportDataEndHandler(k8sData, kp, dbRun, dbData)
		e := echo.New()
		ectx, _ := httptestutil.Post(e, "/api/backends/data/import/end", nil, httptestutil.ContentType("application/json"))
		ectx.SetPath("/api/backends/data/import/end")

		err := testee(ectx)
		if herr := new(echo.HTTPError); !errors.As(err, &herr) {
			t.Fatalf("error is not echo.HTTPError: %s", err)
		} else if herr.Code != http.StatusBadRequest {
			t.Errorf("error code is not %d. actual = %d", http.StatusBadRequest, herr.Code)
		}
	})
}

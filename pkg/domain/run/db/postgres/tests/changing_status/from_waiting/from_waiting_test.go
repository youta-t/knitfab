package from_waiting_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/opst/knitfab/pkg/conn/db/postgres/pool/testenv"
	"github.com/opst/knitfab/pkg/domain"
	"github.com/opst/knitfab/pkg/domain/internal/db/postgres/tables"
	th "github.com/opst/knitfab/pkg/domain/internal/db/postgres/testhelpers"
	. "github.com/opst/knitfab/pkg/domain/run/db/postgres/tests/changing_status/internal"
	"github.com/opst/knitfab/pkg/utils/slices"
	"github.com/opst/knitfab/pkg/utils/try"
)

func Test_ChangingStatus_FromWaiting(t *testing.T) {

	type Expectation struct {
		Statuses  []domain.KnitRunStatus
		Assertion Assertion
	}

	ctx := context.Background()
	poolBroaker := testenv.NewPoolBroaker(ctx, t)
	pool := poolBroaker.GetPool(ctx, t)

	conn := try.To(pool.Acquire(ctx)).OrFatal(t)
	defer conn.Release()

	given := Testdata(
		t,
		try.To(th.PGNow(ctx, conn)).OrFatal(t),
	)

	nth := 0
	for _, testcase := range []struct {
		// (when cursor given,) the run should be subject of state changing.
		when []When

		// try to change picked run's status with them
		change []Expectation
	}{
		{
			when: []When{
				{
					Target: given.ExpectedRun[th.Padding36("run@pseudo-waiting-1")],
					Cursor: domain.RunCursor{
						Status:     []domain.KnitRunStatus{domain.Waiting},
						Pseudo:     []domain.PseudoPlanName{PseudoActive, PseudoInactive},
						PseudoOnly: true,
						Head:       th.Padding36("run@pseudo-waiting-X"),
						Debounce:   time.Hour,
					},
				},
				{
					Target: given.ExpectedRun[th.Padding36("run@pseudo-waiting-2")],
					Cursor: domain.RunCursor{
						Status:     []domain.KnitRunStatus{domain.Waiting},
						Pseudo:     []domain.PseudoPlanName{PseudoActive, PseudoInactive},
						PseudoOnly: true,
						Head:       th.Padding36("run@pseudo-waiting-1"),
						Debounce:   time.Hour,
					},
				},
				{
					Target: given.ExpectedRun[th.Padding36("run@image-waiting-1")],
					Cursor: domain.RunCursor{
						Status:   []domain.KnitRunStatus{domain.Waiting},
						Head:     th.Padding36("run@image-waiting-1"),
						Debounce: time.Hour,
					},
				},
			},
			change: []Expectation{
				{
					Statuses: []domain.KnitRunStatus{
						domain.Waiting, domain.Deactivated, domain.Ready, domain.Aborting,
					},
					Assertion: CanBeChanged,
				},
				{
					Statuses: []domain.KnitRunStatus{
						domain.Starting, domain.Running, domain.Completing, domain.Failed, domain.Done, domain.Invalidated,
					},
					Assertion: ShouldNotBeChanged,
				},
			},
		},
	} {
		for _, target := range testcase.when {
			for _, change := range testcase.change {
				for _, status := range change.Statuses {
					knitIds, _ := slices.Group(
						slices.Map(
							slices.Concat(target.Target.Inputs, target.Target.Outputs),
							func(a domain.Assignment) string { return a.KnitDataBody.KnitId },
						),
						func(s string) bool { return len(s) != 0 },
					)
					if log := target.Target.Log; log != nil && log.KnitDataBody.KnitId != "" {
						knitIds = append(knitIds, log.KnitDataBody.KnitId)
					}

					nth += 1
					t.Run(fmt.Sprintf("#%d", nth), func(t *testing.T) {
						change.Assertion(
							context.Background(), t, poolBroaker,
							[]tables.Operation{given.Plans, given.Runs},
							When{
								Target: target.Target,
								Cursor: target.Cursor,
							},
							Then{
								NewStatus:         status,
								RunIdsToBeLocked:  []string{target.Target.RunBody.Id},
								KnitIdsToBeLocked: knitIds,
							},
						)
					})
				}
			}
		}
	}
}

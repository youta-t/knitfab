package data

import (
	"github.com/opst/knitfab-api-types/data"
	apitags "github.com/opst/knitfab-api-types/tags"
	bindplan "github.com/opst/knitfab/pkg/api-types-binding/plans"
	bindrun "github.com/opst/knitfab/pkg/api-types-binding/runs"
	bindtags "github.com/opst/knitfab/pkg/api-types-binding/tags"
	"github.com/opst/knitfab/pkg/domain"
	"github.com/opst/knitfab/pkg/utils/pointer"
	"github.com/opst/knitfab/pkg/utils/slices"
)

func ComposeSummary(body domain.KnitDataBody) data.Summary {
	return data.Summary{
		KnitId: body.KnitId,
		Tags: slices.Map(
			body.Tags.Slice(),
			func(dt domain.Tag) apitags.Tag { return apitags.Tag{Key: dt.Key, Value: dt.Value} },
		),
	}
}

func composeCreatedFrom(s domain.DataSource) data.CreatedFrom {
	cf := data.CreatedFrom{
		Run: bindrun.ComposeSummary(s.RunBody),
	}

	if s.MountPoint != nil {
		cf.Mountpoint = pointer.Ref(bindplan.ComposeMountpoint(*s.MountPoint))
	}
	cf.Log = bindplan.ComposeLogPoint(s.LogPoint)

	return cf
}

func composeAssignTo(r domain.DataSink) data.AssignedTo {
	return data.AssignedTo{
		Mountpoint: bindplan.ComposeMountpoint(r.MountPoint),
		Run:        bindrun.ComposeSummary(r.RunBody),
	}
}

func composeNominatedBy(n domain.Nomination) data.NominatedBy {
	return data.NominatedBy{
		Mountpoint: bindplan.ComposeMountpoint(n.MountPoint),
		Plan:       bindplan.ComposeSummary(n.PlanBody),
	}
}

func ComposeDetail(d domain.KnitData) data.Detail {
	downstreams, _ := slices.Group(d.Downstreams, func(d domain.DataSink) bool {
		return d.Status != domain.Invalidated
	})

	return data.Detail{
		KnitId:      d.KnitId,
		Tags:        slices.Map(d.Tags.Slice(), bindtags.Compose),
		Upstream:    composeCreatedFrom(d.Upsteram),
		Downstreams: slices.Map(downstreams, composeAssignTo),
		Nomination:  slices.Map(d.NominatedBy, composeNominatedBy),
	}
}

package analytics

import (
	"math"
	"sort"

	"churn/internal/domain"
	"churn/internal/match"
)

// ContentionReport is the §3.3 resource-contention picture: one
// maximum-cardinality assignment of every demand unit — the requirement
// units of ready and resource_blocked ("frontier": deps satisfied, waiting
// only for resources) leaves — onto the free units, computed by the same
// matching engine that defines readiness.
//
// Demand/Matched/Unmet are authoritative. Everything keyed by signature or
// resource is attribution and depends on matching tie-breaks: it is stable
// under the documented tie-break but labeled indicative
// (AttributionIndicative). TagRatios are cruder still — see their doc.
type ContentionReport struct {
	// Demand is the total requirement units demanded; Matched of them fit
	// onto free units simultaneously; Unmet = Demand − Matched is THE
	// authoritative contention number.
	Demand  int
	Matched int
	Unmet   int
	// AttributionIndicative is always true: the per-signature and
	// per-resource splits below are tie-break-dependent attributions of the
	// authoritative totals (§3.3).
	AttributionIndicative bool
	// Signatures aggregates demand by requirement signature (the full
	// AND-set, or the pin), sorted by signature.
	Signatures []SignatureContention
	// Resources lists every resource's free units and the units the
	// assignment takes from it, sorted by resource id.
	Resources []ResourceContention
	// TagRatios are naive per-capability demand/capacity ratios — they
	// double-count multi-capability units and ignore conjunctions, so they
	// are labeled heuristic and never authoritative (§3.3). Sorted by
	// capability id.
	TagRatios []TagRatio
}

// SignatureContention is one requirement signature's slice of the demand.
// A signature wanted by 6 ready things with marginal capacity 2 is a
// flashing light (§3.3).
type SignatureContention struct {
	// Signature is match.Requirement.Signature(): "cap_a+cap_b" or
	// "pin:rs_x".
	Signature string
	// Things are the sorted demanding leaves (ready + resource_blocked).
	Things []string
	// Demand, Matched, Unmet split the report totals (indicative).
	Demand  int
	Matched int
	Unmet   int
	// Pressure is Unmet/Demand in [0,1] — the matching-based pressure the
	// recommendation's scarcity penalty uses (§3.4).
	Pressure float64
}

// ResourceContention is one resource's side of the assignment.
type ResourceContention struct {
	Resource string
	// Free is the clamped free unit count the matching ran against.
	Free int
	// Used is how many of them the assignment took (indicative).
	Used int
	// OverAllocated mirrors the §2.5 resource badge.
	OverAllocated bool
}

// TagRatio is the heuristic at-a-glance indicator for one capability tag.
type TagRatio struct {
	Capability string
	// DemandUnits sums the units of every demand requirement whose AND-set
	// contains the tag; FreeUnits sums the free units of every resource
	// carrying it.
	DemandUnits int
	FreeUnits   int
	// Ratio is DemandUnits/FreeUnits (+Inf when demand meets zero free
	// units, 0 when there is no demand).
	Ratio float64
	// Heuristic is always true: this number double-counts and ignores
	// conjunctions; the matching-based Unmet is the authoritative one.
	Heuristic bool
}

// Contention computes the §3.3 report. Deterministic: demand is gathered in
// (thing id, requirement id) order and matched under the match package's
// documented tie-break.
func Contention(p *domain.Projection) ContentionReport {
	derived, leaves := leafStatuses(p)

	// Demand: requirement units of ready + resource_blocked leaves.
	var reqs []match.Requirement
	thingsBySig := map[string]map[string]struct{}{}
	reqSig := map[string]string{} // requirement id → signature
	for _, id := range leaves {
		st := derived[id].Status
		if st != domain.StatusReady && st != domain.StatusResourceBlocked {
			continue
		}
		for _, r := range p.MatchRequirementsOf(id) {
			reqs = append(reqs, r)
			sig := r.Signature()
			reqSig[r.ID] = sig
			if thingsBySig[sig] == nil {
				thingsBySig[sig] = map[string]struct{}{}
			}
			thingsBySig[sig][id] = struct{}{}
		}
	}
	free := p.FreeResources()
	res := match.Max(reqs, free)

	report := ContentionReport{
		Demand: res.Demand, Matched: res.Matched, Unmet: res.Unmet,
		AttributionIndicative: true,
	}

	// Per-signature attribution.
	bySig := map[string]*SignatureContention{}
	for _, r := range reqs {
		sig := reqSig[r.ID]
		sc := bySig[sig]
		if sc == nil {
			sc = &SignatureContention{Signature: sig, Things: sortedSet(thingsBySig[sig])}
			bySig[sig] = sc
		}
		sc.Demand += r.Quantity
		sc.Unmet += res.UnmetByRequirement[r.ID]
	}
	for _, sig := range sortedIDs(bySig) {
		sc := bySig[sig]
		sc.Matched = sc.Demand - sc.Unmet
		sc.Pressure = float64(sc.Unmet) / float64(sc.Demand)
		report.Signatures = append(report.Signatures, *sc)
	}

	// Per-resource attribution.
	for _, r := range free {
		report.Resources = append(report.Resources, ResourceContention{
			Resource: r.ID, Free: r.Free, Used: res.UsedByResource[r.ID],
			OverAllocated: p.ResourceOverAllocated(r.ID),
		})
	}

	// Heuristic tag ratios.
	demandByTag := map[string]int{}
	for _, r := range reqs {
		for _, c := range r.Capabilities {
			demandByTag[c] += r.Quantity
		}
	}
	freeByTag := map[string]int{}
	for _, r := range free {
		for c := range r.Capabilities {
			freeByTag[c] += r.Free
		}
	}
	for _, c := range sortedIDs(demandByTag) {
		tr := TagRatio{Capability: c, DemandUnits: demandByTag[c], FreeUnits: freeByTag[c], Heuristic: true}
		if tr.FreeUnits > 0 {
			tr.Ratio = float64(tr.DemandUnits) / float64(tr.FreeUnits)
		} else if tr.DemandUnits > 0 {
			tr.Ratio = math.Inf(1)
		}
		report.TagRatios = append(report.TagRatios, tr)
	}
	sort.Slice(report.TagRatios, func(i, j int) bool {
		return report.TagRatios[i].Capability < report.TagRatios[j].Capability
	})
	return report
}

// SignaturePressures returns signature → matching-based pressure from a
// report, the lookup the recommendation's scarcity penalty aggregates over
// (§3.4).
func (r ContentionReport) SignaturePressures() map[string]float64 {
	out := make(map[string]float64, len(r.Signatures))
	for _, s := range r.Signatures {
		out[s.Signature] = s.Pressure
	}
	return out
}

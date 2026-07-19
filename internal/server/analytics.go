package server

import (
	"math"
	"net/http"

	"churn/internal/analytics"
	"churn/internal/domain"
	"churn/internal/match"
)

// ── recommendation / ready DTOs ──

type termDTO struct {
	Name         string  `json:"name"`
	Value        float64 `json:"value"`
	Weight       float64 `json:"weight"`
	Contribution float64 `json:"contribution"`
	Detail       string  `json:"detail"`
}

type recommendationDTO struct {
	Thing string    `json:"thing"`
	Score float64   `json:"score"`
	Terms []termDTO `json:"terms"`
}

func buildRecommendationDTO(r analytics.Recommendation) recommendationDTO {
	terms := make([]termDTO, len(r.Terms))
	for i, t := range r.Terms {
		terms[i] = termDTO{Name: t.Name, Value: t.Value, Weight: t.Weight,
			Contribution: t.Contribution, Detail: t.Detail}
	}
	return recommendationDTO{Thing: r.Thing, Score: r.Score, Terms: terms}
}

type readyEntryDTO struct {
	Thing        string            `json:"thing"`
	Project      string            `json:"project"`
	Type         string            `json:"type"`
	Requirements []matchReqDTO     `json:"requirements"`
	Score        recommendationDTO `json:"score"`
}

// getReady implements GET /api/v1/analytics/ready with the §3.1 filters as
// query params: project, type, subtree, capability. Sorted like Recommend:
// score descending, then thing id.
func (s *Server) getReady(rw http.ResponseWriter, r *http.Request) {
	settings, e := s.settings.load()
	if e != nil {
		writeError(rw, e)
		return
	}
	q := r.URL.Query()
	entries := analytics.Ready(s.w.Projection(), settings, analytics.ReadyFilter{
		Project:    q.Get("project"),
		Type:       q.Get("type"),
		Subtree:    q.Get("subtree"),
		Capability: q.Get("capability"),
	})
	out := make([]readyEntryDTO, 0, len(entries))
	for _, en := range entries {
		out = append(out, readyEntryDTO{
			Thing: en.Thing, Project: en.Project, Type: en.Type,
			Requirements: buildMatchReqDTOs(en.Requirements),
			Score:        buildRecommendationDTO(en.Score),
		})
	}
	writeJSON(rw, http.StatusOK, out)
}

// getRecommendations implements GET /api/v1/analytics/recommendations: the
// §3.4 ranking with full explanations and the live weights that produced it.
func (s *Server) getRecommendations(rw http.ResponseWriter, r *http.Request) {
	settings, e := s.settings.load()
	if e != nil {
		writeError(rw, e)
		return
	}
	recs := analytics.Recommend(s.w.Projection(), settings)
	out := make([]recommendationDTO, 0, len(recs))
	for _, rec := range recs {
		out = append(out, buildRecommendationDTO(rec))
	}
	writeJSON(rw, http.StatusOK, struct {
		Weights         weightsDTO          `json:"weights"`
		Recommendations []recommendationDTO `json:"recommendations"`
	}{buildWeightsDTO(settings), out})
}

// ── bottlenecks ──

type criticalityDTO struct {
	Thing           string `json:"thing"`
	DownstreamReach int    `json:"downstream_reach"`
	ImmediateUnlock int    `json:"immediate_unlock"`
	RemainingDepth  int    `json:"remaining_depth"`
}

type signatureContentionDTO struct {
	Signature string   `json:"signature"`
	Things    []string `json:"things"`
	Demand    int      `json:"demand"`
	Matched   int      `json:"matched"`
	Unmet     int      `json:"unmet"`
	Pressure  float64  `json:"pressure"`
}

type resourceContentionDTO struct {
	Resource      string `json:"resource"`
	Free          int    `json:"free"`
	Used          int    `json:"used"`
	OverAllocated bool   `json:"over_allocated"`
}

type tagRatioDTO struct {
	Capability  string `json:"capability"`
	DemandUnits int    `json:"demand_units"`
	FreeUnits   int    `json:"free_units"`
	// Ratio is null when demand meets zero free units (the +Inf case, which
	// JSON cannot carry).
	Ratio     *float64 `json:"ratio"`
	Heuristic bool     `json:"heuristic"`
}

type contentionDTO struct {
	Demand                int                      `json:"demand"`
	Matched               int                      `json:"matched"`
	Unmet                 int                      `json:"unmet"`
	AttributionIndicative bool                     `json:"attribution_indicative"`
	Signatures            []signatureContentionDTO `json:"signatures"`
	Resources             []resourceContentionDTO  `json:"resources"`
	TagRatios             []tagRatioDTO            `json:"tag_ratios"`
}

func buildContentionDTO(c analytics.ContentionReport) contentionDTO {
	dto := contentionDTO{
		Demand: c.Demand, Matched: c.Matched, Unmet: c.Unmet,
		AttributionIndicative: c.AttributionIndicative,
		Signatures:            []signatureContentionDTO{},
		Resources:             []resourceContentionDTO{},
		TagRatios:             []tagRatioDTO{},
	}
	for _, sc := range c.Signatures {
		dto.Signatures = append(dto.Signatures, signatureContentionDTO{
			Signature: sc.Signature, Things: sc.Things,
			Demand: sc.Demand, Matched: sc.Matched, Unmet: sc.Unmet, Pressure: sc.Pressure,
		})
	}
	for _, rc := range c.Resources {
		dto.Resources = append(dto.Resources, resourceContentionDTO{
			Resource: rc.Resource, Free: rc.Free, Used: rc.Used, OverAllocated: rc.OverAllocated,
		})
	}
	for _, tr := range c.TagRatios {
		d := tagRatioDTO{Capability: tr.Capability, DemandUnits: tr.DemandUnits,
			FreeUnits: tr.FreeUnits, Heuristic: tr.Heuristic}
		if !math.IsInf(tr.Ratio, 1) {
			ratio := tr.Ratio
			d.Ratio = &ratio
		}
		dto.TagRatios = append(dto.TagRatios, d)
	}
	return dto
}

type starvationDTO struct {
	Thing string `json:"thing"`
	// Durations in whole seconds — cumulative credit per §3.4 (retained
	// across the ready flip), current stint per §3.3.
	CurrentStintSeconds float64 `json:"current_stint_seconds"`
	CreditSeconds       float64 `json:"credit_seconds"`
}

// getBottlenecks implements GET /api/v1/analytics/bottlenecks: the §3.3
// picture — criticality triple per thing (sorted by id; dashboards re-sort),
// the matching-based contention report, and the starvation list (sorted by
// current stint, then credit, then id).
func (s *Server) getBottlenecks(rw http.ResponseWriter, _ *http.Request) {
	p := s.w.Projection()
	crits := analytics.Criticalities(p)
	critDTOs := make([]criticalityDTO, 0, len(crits))
	for _, c := range crits {
		critDTOs = append(critDTOs, criticalityDTO{
			Thing: c.Thing, DownstreamReach: c.DownstreamReach,
			ImmediateUnlock: c.ImmediateUnlock, RemainingDepth: c.RemainingDepth,
		})
	}
	starves := analytics.Starvations(p)
	starveDTOs := make([]starvationDTO, 0, len(starves))
	for _, st := range starves {
		starveDTOs = append(starveDTOs, starvationDTO{
			Thing:               st.Thing,
			CurrentStintSeconds: st.CurrentStint.Seconds(),
			CreditSeconds:       st.Credit.Seconds(),
		})
	}
	writeJSON(rw, http.StatusOK, struct {
		Criticality []criticalityDTO `json:"criticality"`
		Contention  contentionDTO    `json:"contention"`
		Starvation  []starvationDTO  `json:"starvation"`
	}{critDTOs, buildContentionDTO(analytics.Contention(p)), starveDTOs})
}

// ── resource board ──

type boardAllocationDTO struct {
	ID          string `json:"id"`
	Thing       string `json:"thing"`
	ThingName   string `json:"thing_name"`
	Requirement string `json:"requirement"`
	Quantity    int    `json:"quantity"`
}

type boardQueueEntryDTO struct {
	Thing  string `json:"thing"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// Requirements lists the thing's requirement ids this resource is
	// eligible for (the reason it is in this queue).
	Requirements []string `json:"requirements"`
}

type resourceBoardRowDTO struct {
	Resource resourceDTO `json:"resource"`
	// OpenAllocations is the current work on the resource, sorted by
	// allocation id; Queue the ready/resource-blocked things wanting it,
	// sorted by thing id.
	OpenAllocations []boardAllocationDTO `json:"open_allocations"`
	Queue           []boardQueueEntryDTO `json:"queue"`
}

// getResourceBoard implements GET /api/v1/analytics/resource-board: one row
// per resource (sorted by id) with capacity, effective and free units, the
// open allocations, and the queue of ready or resource-blocked leaves with
// at least one requirement this resource is eligible for — eligibility via
// the same match engine that defines readiness (the one-brain rule).
func (s *Server) getResourceBoard(rw http.ResponseWriter, _ *http.Request) {
	p := s.w.Projection()
	derived := p.DeriveAll()

	// Demand side, computed once: ready/resource-blocked leaves and their
	// matcher requirements.
	type demandLeaf struct {
		id     string
		status domain.Status
		reqs   []match.Requirement
	}
	var demand []demandLeaf
	for _, tid := range sortedKeys(p.Things) {
		st := derived[tid].Status
		if st != domain.StatusReady && st != domain.StatusResourceBlocked {
			continue
		}
		reqs := p.MatchRequirementsOf(tid)
		if len(reqs) == 0 {
			continue
		}
		demand = append(demand, demandLeaf{id: tid, status: st, reqs: reqs})
	}

	rows := make([]resourceBoardRowDTO, 0, len(p.Resources))
	for _, rid := range sortedKeys(p.Resources) {
		rs := p.Resources[rid]
		row := resourceBoardRowDTO{
			Resource:        buildResourceDTO(p, rid),
			OpenAllocations: []boardAllocationDTO{},
			Queue:           []boardQueueEntryDTO{},
		}
		for _, aid := range sortedKeys(p.Allocations) {
			al := p.Allocations[aid]
			if !al.Open || al.Resource != rid {
				continue
			}
			name := ""
			if th, ok := p.Things[al.Thing]; ok {
				name = th.Name
			}
			row.OpenAllocations = append(row.OpenAllocations, boardAllocationDTO{
				ID: aid, Thing: al.Thing, ThingName: name,
				Requirement: al.Requirement, Quantity: al.Quantity,
			})
		}
		unit := match.Resource{ID: rid, Free: 1, Capabilities: rs.Capabilities}
		for _, d := range demand {
			var wanting []string
			for _, req := range d.reqs {
				if match.Eligible(req, unit) {
					wanting = append(wanting, req.ID)
				}
			}
			if len(wanting) == 0 {
				continue
			}
			row.Queue = append(row.Queue, boardQueueEntryDTO{
				Thing: d.id, Name: p.Things[d.id].Name,
				Status: string(d.status), Requirements: wanting,
			})
		}
		rows = append(rows, row)
	}
	writeJSON(rw, http.StatusOK, rows)
}

package match

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// oracleMax is an exhaustive maximum-matching oracle, deliberately
// independent of the production algorithm: it tries, unit by unit, every
// eligible resource (and skipping), memoized on (unit index, remaining free
// vector). Small instances only.
func oracleMax(reqs []Requirement, resources []Resource) int {
	rs := append([]Requirement(nil), reqs...)
	sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
	res := append([]Resource(nil), resources...)
	sort.Slice(res, func(i, j int) bool { return res[i].ID < res[j].ID })

	var units []int // requirement index per unit
	for i, r := range rs {
		for u := 0; u < r.Quantity; u++ {
			units = append(units, i)
		}
	}
	free := make([]int, len(res))
	for j, r := range res {
		free[j] = r.Free
	}
	memo := map[string]int{}
	var solve func(k int) int
	solve = func(k int) int {
		if k == len(units) {
			return 0
		}
		key := fmt.Sprint(k, free)
		if v, ok := memo[key]; ok {
			return v
		}
		best := solve(k + 1) // leave this unit unmatched
		for j := range res {
			if free[j] > 0 && Eligible(rs[units[k]], res[j]) {
				free[j]--
				if v := 1 + solve(k+1); v > best {
					best = v
				}
				free[j]++
			}
		}
		memo[key] = best
		return best
	}
	return solve(0)
}

// randomInstance builds a small random instance: ≤8 requirement units and
// ≤8 resource units, random capability sets, including pins.
func randomInstance(rng *rand.Rand) ([]Requirement, []Resource) {
	caps := []string{"cap_a", "cap_b", "cap_c"}
	pick := func() map[string]struct{} {
		s := map[string]struct{}{}
		for _, c := range caps {
			if rng.Intn(2) == 0 {
				s[c] = struct{}{}
			}
		}
		return s
	}

	nRes := 1 + rng.Intn(4)
	var resources []Resource
	unitsLeft := 8
	for j := 0; j < nRes; j++ {
		free := rng.Intn(3) // 0 is legal: an exhausted resource
		if free > unitsLeft {
			free = unitsLeft
		}
		unitsLeft -= free
		resources = append(resources, Resource{
			ID: fmt.Sprintf("rs_%02d", j), Free: free, Capabilities: pick(),
		})
	}

	nReq := 1 + rng.Intn(4)
	var reqs []Requirement
	demandLeft := 8
	for i := 0; i < nReq && demandLeft > 0; i++ {
		r := Requirement{ID: fmt.Sprintf("req_%02d", i)}
		if rng.Intn(4) == 0 { // pin
			r.Pin = resources[rng.Intn(len(resources))].ID
			r.Quantity = 1
		} else {
			for c := range pick() {
				r.Capabilities = append(r.Capabilities, c)
			}
			sort.Strings(r.Capabilities)
			if len(r.Capabilities) == 0 {
				r.Capabilities = []string{caps[rng.Intn(len(caps))]}
			}
			r.Quantity = 1 + rng.Intn(3)
		}
		if r.Quantity > demandLeft {
			r.Quantity = demandLeft
		}
		demandLeft -= r.Quantity
		reqs = append(reqs, r)
	}
	return reqs, resources
}

// TestMaxMatchesOracle compares the production matcher against the
// exhaustive oracle on many random small instances: the maximum cardinality
// must be identical, and the feasibility verdict must agree with
// "every unit matched".
func TestMaxMatchesOracle(t *testing.T) {
	for seed := int64(0); seed < 500; seed++ {
		rng := rand.New(rand.NewSource(seed))
		reqs, resources := randomInstance(rng)
		got := Max(reqs, resources)
		want := oracleMax(reqs, resources)
		if got.Matched != want {
			t.Fatalf("seed %d: Matched = %d, oracle = %d\nreqs %+v\nres %+v",
				seed, got.Matched, want, reqs, resources)
		}
		if got.Demand-got.Matched != got.Unmet {
			t.Fatalf("seed %d: Demand %d, Matched %d, Unmet %d inconsistent",
				seed, got.Demand, got.Matched, got.Unmet)
		}
		if _, ok := Feasible(reqs, resources); ok != (want == got.Demand) {
			t.Fatalf("seed %d: Feasible = %v, oracle demand %d matched %d",
				seed, ok, got.Demand, want)
		}
	}
}

// TestMaxAssignmentConsistent verifies the internal accounting of the
// attribution: assignment cells sum to Matched, respect eligibility and
// free capacity, and agree with the per-requirement/per-resource maps.
func TestMaxAssignmentConsistent(t *testing.T) {
	for seed := int64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		reqs, resources := randomInstance(rng)
		r := Max(reqs, resources)

		byReq := map[string]int{}
		byRes := map[string]int{}
		total := 0
		reqByID := map[string]Requirement{}
		for _, q := range reqs {
			reqByID[q.ID] = q
		}
		resByID := map[string]Resource{}
		for _, u := range resources {
			resByID[u.ID] = u
		}
		for _, a := range r.Assignment {
			if a.Units <= 0 {
				t.Fatalf("seed %d: zero cell %+v", seed, a)
			}
			if !Eligible(reqByID[a.Requirement], resByID[a.Resource]) {
				t.Fatalf("seed %d: ineligible cell %+v", seed, a)
			}
			byReq[a.Requirement] += a.Units
			byRes[a.Resource] += a.Units
			total += a.Units
		}
		if total != r.Matched {
			t.Fatalf("seed %d: cells sum %d, Matched %d", seed, total, r.Matched)
		}
		for _, q := range reqs {
			if byReq[q.ID] > q.Quantity {
				t.Fatalf("seed %d: requirement %s over-served", seed, q.ID)
			}
			if r.UnmetByRequirement[q.ID] != q.Quantity-byReq[q.ID] {
				t.Fatalf("seed %d: UnmetByRequirement[%s] = %d, want %d",
					seed, q.ID, r.UnmetByRequirement[q.ID], q.Quantity-byReq[q.ID])
			}
		}
		for _, u := range resources {
			if byRes[u.ID] > u.Free {
				t.Fatalf("seed %d: resource %s over-used (%d > %d)", seed, u.ID, byRes[u.ID], u.Free)
			}
			if r.UsedByResource[u.ID] != byRes[u.ID] {
				t.Fatalf("seed %d: UsedByResource[%s] = %d, want %d",
					seed, u.ID, r.UsedByResource[u.ID], byRes[u.ID])
			}
		}
	}
}

// TestMaxDeterministic: the same instance — in any input order — yields the
// byte-identical result, twice over.
func TestMaxDeterministic(t *testing.T) {
	for seed := int64(0); seed < 100; seed++ {
		rng := rand.New(rand.NewSource(seed))
		reqs, resources := randomInstance(rng)
		a := Max(reqs, resources)
		b := Max(reqs, resources)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("seed %d: two runs differ", seed)
		}
		// Shuffle the inputs: the documented tie-break sorts by id first, so
		// input order must not leak into the result.
		sr := append([]Requirement(nil), reqs...)
		su := append([]Resource(nil), resources...)
		rng.Shuffle(len(sr), func(i, j int) { sr[i], sr[j] = sr[j], sr[i] })
		rng.Shuffle(len(su), func(i, j int) { su[i], su[j] = su[j], su[i] })
		if c := Max(sr, su); !reflect.DeepEqual(a, c) {
			t.Fatalf("seed %d: shuffled input changed the result", seed)
		}
	}
}

// TestAugmentingBeatsGreedy pins the case that killed the greedy
// per-requirement check (DESIGN.md §2.4): one multi-capability unit is the
// only candidate for two requirements. Greedy would call each requirement
// individually satisfiable; the matching must route around it when a second
// unit exists, and must say infeasible when it does not.
func TestAugmentingBeatsGreedy(t *testing.T) {
	reqA := Requirement{ID: "req_a", Quantity: 1, Capabilities: []string{"cap_a"}}
	reqB := Requirement{ID: "req_b", Quantity: 1, Capabilities: []string{"cap_b"}}
	both := Resource{ID: "rs_both", Free: 1, Capabilities: map[string]struct{}{"cap_a": {}, "cap_b": {}}}
	onlyB := Resource{ID: "rs_only_b", Free: 1, Capabilities: map[string]struct{}{"cap_b": {}}}

	// Only the dual-capability unit: infeasible for both at once.
	if _, ok := Feasible([]Requirement{reqA, reqB}, []Resource{both}); ok {
		t.Fatal("one unit cannot serve two requirements")
	}
	// A second, B-only unit: feasible, and the assignment must route req_a
	// to the dual unit and req_b to the B-only unit (augmenting path).
	asg, ok := Feasible([]Requirement{reqA, reqB}, []Resource{both, onlyB})
	if !ok {
		t.Fatal("must be feasible with the second unit")
	}
	want := []Assigned{{"req_a", "rs_both", 1}, {"req_b", "rs_only_b", 1}}
	if !reflect.DeepEqual(asg, want) {
		t.Fatalf("assignment = %+v, want %+v", asg, want)
	}
}

// TestPinEligibility: a pin is satisfied only by the pinned resource,
// regardless of capabilities.
func TestPinEligibility(t *testing.T) {
	pin := Requirement{ID: "req_p", Quantity: 1, Pin: "rs_anna"}
	anna := Resource{ID: "rs_anna", Free: 1}
	other := Resource{ID: "rs_bob", Free: 5, Capabilities: map[string]struct{}{"cap_x": {}}}
	if _, ok := Feasible([]Requirement{pin}, []Resource{other}); ok {
		t.Fatal("pin must not match another resource")
	}
	asg, ok := Feasible([]Requirement{pin}, []Resource{anna, other})
	if !ok || !reflect.DeepEqual(asg, []Assigned{{"req_p", "rs_anna", 1}}) {
		t.Fatalf("pin assignment = %+v ok=%v", asg, ok)
	}
}

func TestSignature(t *testing.T) {
	r := Requirement{ID: "r", Capabilities: []string{"cap_b", "cap_a"}}
	if got := r.Signature(); got != "cap_a+cap_b" {
		t.Fatalf("Signature = %q", got)
	}
	p := Requirement{ID: "r", Pin: "rs_anna"}
	if got := p.Signature(); got != "pin:rs_anna" {
		t.Fatalf("pin Signature = %q", got)
	}
}

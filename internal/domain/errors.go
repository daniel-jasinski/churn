package domain

import (
	"fmt"
	"strings"
)

// Error kinds — the machine-readable classification of a batch rejection.
// The API layer serializes {Kind, Message, IDs} directly (PLAN.md M5).
const (
	// KindStaleVersion: an expected_versions precondition failed; IDs are the
	// stale entity ids.
	KindStaleVersion = "stale_version"
	// KindCycle: the batch would make the expanded leaf graph cyclic; IDs are
	// the things of the offending expanded cycle, in order.
	KindCycle = "cycle"
	// KindDuplicateID: a create/define/assert reuses an existing id.
	KindDuplicateID = "duplicate_id"
	// KindUnknownEntity: the event's target entity does not exist.
	KindUnknownEntity = "unknown_entity"
	// KindUndefinedReference: the payload references an undeclared vocabulary
	// entry or a missing entity (declared-before-use, §5.3).
	KindUndefinedReference = "undefined_reference"
	// KindRetractionBlocked: retraction while inbound references exist; IDs
	// enumerate the referencing entities.
	KindRetractionBlocked = "retraction_blocked"
	// KindSemanticImmutable: a state's semantic change while things are in
	// it; IDs are the occupying things.
	KindSemanticImmutable = "semantic_immutable"
	// KindContainment: containment violations — cross-project parenting,
	// parent-chain cycles, parenting under an ineligible leaf (§2.1).
	KindContainment = "containment"
	// KindCompositeState: a state transition targeting a composite.
	KindCompositeState = "composite_state"
	// KindCompositeRequirement: a requirement on a composite.
	KindCompositeRequirement = "composite_requirement"
	// KindPinViolation: pin rules (§2.4) — pinning an unnamed resource, or a
	// pinned resource losing named.
	KindPinViolation = "pin_violation"
	// KindAllocation: allocation lifecycle violations — opening for a
	// non-active thing, closing a non-open allocation, wrong requirement.
	KindAllocation = "allocation"
	// KindAllocationCoverage: entering active semantics without quantity-
	// exact allocation coverage, or leaving without closing all allocations.
	KindAllocationCoverage = "allocation_coverage"
	// KindCapacity: an opened allocation exceeds free effective capacity.
	KindCapacity = "capacity"
	// KindInfeasible: an opened allocation's resource cannot satisfy the
	// requirement it names — it lacks part of the capability AND-set, or is
	// not the pinned resource (§2.4).
	KindInfeasible = "infeasible_allocation"
	// KindDemotion: a demotion without the explicitly appended transition
	// into a pending-semantic state (§2.1).
	KindDemotion = "demotion"
)

// Error is the structured rejection every §5.2 invariant reports: a
// machine-readable Kind, a human-readable Message, and the entity ids the
// violation is about (sorted unless the kind defines an order, as the cycle
// kind does).
type Error struct {
	Kind    string
	Message string
	IDs     []string
}

// Error implements error.
func (e *Error) Error() string {
	if len(e.IDs) == 0 {
		return fmt.Sprintf("domain: %s: %s", e.Kind, e.Message)
	}
	return fmt.Sprintf("domain: %s: %s [%s]", e.Kind, e.Message, strings.Join(e.IDs, ", "))
}

func errf(kind string, ids []string, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...), IDs: ids}
}

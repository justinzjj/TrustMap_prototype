package replay

import (
	"math"
	"testing"
)

func TestCostProfileBuiltinsAreExactAndFingerprintStable(t *testing.T) {
	legacy := LegacyV41CostProfile()
	if legacy.ID != "legacy-v4.1" || legacy.DirectStepCost != 3_000_000 || legacy.PathStepCost != 30_000 || legacy.TrustRootUpdateCost != 110_000 {
		t.Fatalf("legacy profile = %#v", legacy)
	}
	calibrated := PrototypeCalibratedCostProfile()
	if calibrated.ID != "prototype-calibrated" || calibrated.DirectStepCost != 3_000_096 || calibrated.PathStepCost != 30_713 || calibrated.TrustRootUpdateCost != 108_582 {
		t.Fatalf("calibrated profile = %#v", calibrated)
	}
	if legacy.Fingerprint() == "" || legacy.Fingerprint() != LegacyV41CostProfile().Fingerprint() || legacy.Fingerprint() == calibrated.Fingerprint() {
		t.Fatal("profile fingerprints are not stable/distinct")
	}
}

func TestCostProfileRejectsInvalidAndCheckedCostOverflow(t *testing.T) {
	for _, profile := range []CostProfile{
		{},
		{ID: "x", DirectStepCost: 1, PathStepCost: 1},
		{ID: "x", DirectStepCost: 1, TrustRootUpdateCost: 1},
		{ID: "x", PathStepCost: 1, TrustRootUpdateCost: 1},
	} {
		if err := profile.Validate(); err == nil {
			t.Fatalf("invalid profile accepted: %#v", profile)
		}
	}
	if _, err := CheckedCost(math.MaxUint64, 2); err == nil {
		t.Fatal("overflowing cost accepted")
	}
	if got, err := CheckedCost(123, 3_000_000); err != nil || got != 369_000_000 {
		t.Fatalf("CheckedCost() = %d, %v", got, err)
	}
	if _, err := CheckedAdd(math.MaxUint64, 1); err == nil {
		t.Fatal("overflowing addition accepted")
	}
}

func TestConfigCostPreflightRejectsTraceBoundOverflow(t *testing.T) {
	trace := PreparedTrace{
		Chains:      []string{"a", "b"},
		InitHeights: map[string]uint64{"a": 0, "b": 0},
		Events: []ReplayEvent{{
			Source:      ReplayBlock{Chain: "a", OriginalHeight: 2},
			Destination: ReplayBlock{Chain: "b", OriginalHeight: 1},
		}},
	}
	config := Config{CostProfile: CostProfile{ID: "overflow", DirectStepCost: math.MaxUint64, PathStepCost: 1, TrustRootUpdateCost: 1}}
	if err := config.ValidatePreparedTrace(trace); err == nil {
		t.Fatal("overflow-prone trace/profile combination accepted")
	}
	config.CostProfile = LegacyV41CostProfile()
	if err := config.ValidatePreparedTrace(trace); err != nil {
		t.Fatalf("valid trace/profile rejected: %v", err)
	}
}

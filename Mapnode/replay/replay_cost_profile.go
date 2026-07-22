package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
)

// CostProfile holds integer replay-only cost estimates. These values are not
// claims that replay performs equivalent on-chain verification.
type CostProfile struct {
	ID                  string `yaml:"id" json:"id"`
	DirectStepCost      uint64 `yaml:"direct_step_cost" json:"direct_step_cost"`
	PathStepCost        uint64 `yaml:"path_step_cost" json:"path_step_cost"`
	TrustRootUpdateCost uint64 `yaml:"trust_root_update_cost" json:"trust_root_update_cost"`
}

func LegacyV41CostProfile() CostProfile {
	return CostProfile{ID: "legacy-v4.1", DirectStepCost: 3_000_000, PathStepCost: 30_000, TrustRootUpdateCost: 110_000}
}

func PrototypeCalibratedCostProfile() CostProfile {
	return CostProfile{ID: "prototype-calibrated", DirectStepCost: 3_000_096, PathStepCost: 30_713, TrustRootUpdateCost: 108_582}
}

func (profile CostProfile) Validate() error {
	if strings.TrimSpace(profile.ID) == "" {
		return fmt.Errorf("cost profile id is required")
	}
	if profile.DirectStepCost == 0 || profile.PathStepCost == 0 || profile.TrustRootUpdateCost == 0 {
		return fmt.Errorf("all cost profile values must be positive")
	}
	return nil
}

func (profile CostProfile) Fingerprint() string {
	material := fmt.Sprintf("trustmap/replay-cost-profile/v1\n%s\n%d\n%d\n%d", profile.ID, profile.DirectStepCost, profile.PathStepCost, profile.TrustRootUpdateCost)
	digest := sha256.Sum256([]byte(material))
	return hex.EncodeToString(digest[:])
}

func CheckedCost(delta, step uint64) (uint64, error) {
	if delta != 0 && step > math.MaxUint64/delta {
		return 0, fmt.Errorf("cost overflow: %d * %d", delta, step)
	}
	return delta * step, nil
}

func CheckedAdd(left, right uint64) (uint64, error) {
	if right > math.MaxUint64-left {
		return 0, fmt.Errorf("cost overflow: %d + %d", left, right)
	}
	return left + right, nil
}

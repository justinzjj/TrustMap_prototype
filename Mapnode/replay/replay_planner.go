package replay

type ReplayPlanChoice struct {
	UseTrustMap bool
	PathFound   bool
	Path        ReplayPath
	TotalCost   uint64
}

type ReplayPlanner struct{ Profile CostProfile }

func (planner ReplayPlanner) Plan(view *ReplayTrustView, start, goal ReplayBlockKey, directCost uint64) (ReplayPlanChoice, error) {
	if directCost <= planner.Profile.TrustRootUpdateCost {
		return ReplayPlanChoice{TotalCost: directCost}, nil
	}
	cutoff := directCost - planner.Profile.TrustRootUpdateCost
	path, found, err := view.ShortestPath(start, goal, cutoff, goal.Chain, goal.Height)
	if err != nil {
		return ReplayPlanChoice{}, err
	}
	return planner.choose(directCost, path, found)
}

func (planner ReplayPlanner) choose(directCost uint64, path ReplayPath, found bool) (ReplayPlanChoice, error) {
	choice := ReplayPlanChoice{PathFound: found, Path: path, TotalCost: directCost}
	if !found {
		return choice, nil
	}
	total, err := CheckedAdd(path.Cost, planner.Profile.TrustRootUpdateCost)
	if err != nil {
		return ReplayPlanChoice{}, err
	}
	if total < directCost {
		choice.UseTrustMap = true
		choice.TotalCost = total
	}
	return choice, nil
}

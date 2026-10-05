package main

import "errors"

func documentEvents(events []M, key, id string) []M {
	out := []M{}
	for _, e := range events {
		if str(e, key) == id {
			out = append(out, e)
		}
	}
	return out
}
func financialStatus(a M, events []M) string {
	submitted, reviewed := false, false
	var su, partner M
	legacyManager, legacyFinance := false, false
	for _, e := range events {
		switch str(e, "action") {
		case "reject":
			return "Rejected"
		case "submit":
			submitted = true
		case "review":
			reviewed = true
		case "approve":
			switch str(e, "stage") {
			case "superuser":
				su = e
			case "admin":
				partner = e
			case "finance":
				partner = e
				legacyFinance = true
			case "manager":
				legacyManager = true
			}
		}
	}
	if number(a, "approvalPolicy") != 2 {
		if legacyFinance {
			return "Approved"
		}
		if legacyManager {
			return "Finance approval"
		}
		if reviewed {
			return "Manager approval"
		}
		if submitted {
			return "Review"
		}
		return "Draft"
	}
	own, _ := a["creatorSuperuser"].(bool)
	if su != nil && (own || (partner != nil && str(partner, "actorId") != str(su, "actorId"))) {
		return "Approved"
	}
	if !submitted {
		return "Draft"
	}
	if own {
		return "Superuser approval"
	}
	if !reviewed {
		return "Review"
	}
	if su != nil {
		return "Admin / Finance approval"
	}
	if partner != nil {
		return "Superuser approval"
	}
	return "Admin / Finance + Superuser"
}
func financialApprovalStage(s State, u M, t string, a M, events []M, form string) (string, error) {
	st := financialStatus(a, events)
	if !can(s, u, t, "approve", form) {
		return "", errors.New("Approval permission is required")
	}
	if str(a, "createdBy") == str(u, "id") && !admin(u) {
		return "", errors.New("The creator cannot approve their own submission")
	}
	if st == "Draft" || st == "Approved" || st == "Rejected" {
		return "", errors.New("Only pending submitted records can be approved")
	}
	for _, e := range events {
		if str(e, "action") == "approve" && str(e, "actorId") == str(u, "id") && (number(a, "approvalPolicy") != 2 || str(e, "stage") != "manager") {
			return "", errors.New("Two different approvers are required")
		}
	}
	if number(a, "approvalPolicy") != 2 {
		stage := ""
		if st == "Manager approval" {
			stage = "manager"
		} else if st == "Finance approval" {
			stage = "finance"
		}
		if stage != "" && hasStage(s, u, t, stage) {
			return stage, nil
		}
		return "", errors.New("Review and the matching approval role are required")
	}
	own, _ := a["creatorSuperuser"].(bool)
	if own {
		if admin(u) {
			return "superuser", nil
		}
		return "", errors.New("Superuser submissions require only superuser approval")
	}
	reviewed, hasSU, hasPartner := false, false, false
	for _, e := range events {
		if str(e, "action") == "review" {
			reviewed = true
		}
		if str(e, "action") == "approve" {
			if str(e, "stage") == "superuser" {
				hasSU = true
			}
			if str(e, "stage") == "admin" || str(e, "stage") == "finance" {
				hasPartner = true
			}
		}
	}
	if !reviewed {
		return "", errors.New("Review is required before approval")
	}
	if admin(u) && !hasSU {
		return "superuser", nil
	}
	if !admin(u) && !hasPartner {
		for _, r := range assigned(s, u, t) {
			stage := str(r, "stage")
			if (stage == "admin" || stage == "finance") && contains(arr(obj(obj(r, "permissions"), form), "actions"), "approve") {
				return stage, nil
			}
		}
	}
	return "", errors.New("Approval requires Superuser plus an Admin or Finance approver")
}

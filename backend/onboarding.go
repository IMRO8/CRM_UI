package main

import (
	"database/sql"
	"errors"
	"regexp"
	"strings"
)

var onboardingFields = []string{"name", "trade", "vendorType", "contactName", "phone", "email", "gstin", "pan", "address", "notes"}

func onboardingRecord(s State, u M, t string, d M) (M, error) {
	old := find(s["vendors"], str(d, "id"))
	action := "create"
	if str(d, "id") != "" {
		action = "edit"
		if old == nil || str(old, "tenantId") != t || old["deleted"] == true {
			return nil, errors.New("Vendor not found")
		}
		if str(old, "onboardingStatus") != "Draft" || (!admin(u) && str(old, "createdBy") != str(u, "id")) {
			return nil, errors.New("Only creator or superuser can edit an onboarding draft")
		}
	}
	if !can(s, u, t, action, "vendor_onboarding") || !fieldsEditable(s, u, t, "vendor_onboarding", onboardingFields) {
		return nil, errors.New("Vendor onboarding field editing permission is required")
	}
	m := M{"id": uuid(), "tenantId": t, "deleted": false, "createdBy": str(u, "id"), "createdAt": now(), "updatedAt": now(), "onboardingStatus": "Draft"}
	if old != nil {
		for _, k := range []string{"id", "createdBy", "createdAt"} {
			m[k] = old[k]
		}
	}
	for _, k := range onboardingFields {
		v := strings.TrimSpace(str(d, k))
		if len(v) > 1000 {
			return nil, errors.New("Vendor details exceed allowed length")
		}
		m[k] = v
	}
	digits := regexp.MustCompile(`\D`).ReplaceAllString(str(m, "phone"), "")
	if str(m, "name") == "" || str(m, "trade") == "" || str(m, "vendorType") == "" || str(m, "contactName") == "" || str(m, "address") == "" || len(digits) < 7 || len(digits) > 15 || !regexp.MustCompile(`^\+?[0-9 ()-]+$`).MatchString(str(m, "phone")) {
		return nil, errors.New("Name, type, trade, contact, address and valid phone are required")
	}
	if str(m, "email") != "" && !regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`).MatchString(str(m, "email")) {
		return nil, errors.New("Enter a valid email")
	}
	for _, k := range []string{"gstin", "pan"} {
		m[k] = strings.ToUpper(str(m, k))
		n := 15
		if k == "pan" {
			n = 10
		}
		if str(m, k) != "" && (len(str(m, k)) != n || !regexp.MustCompile(`^[A-Z0-9]+$`).MatchString(str(m, k))) {
			return nil, errors.New("Tax reference has invalid format")
		}
	}
	if str(m, "gstin") == "" && str(m, "pan") == "" {
		return nil, errors.New("PAN or GSTIN is required")
	}
	data, version, e := financialExtras(s, u, t, d, "vendor_onboarding", old)
	if e != nil {
		return nil, e
	}
	m["data"] = data
	m["formVersion"] = version
	return m, nil
}
func manageOnboarding(tx *sql.Tx, s State, u M, t, kind string, d M) (M, error) {
	var v M
	var err error
	action := "draft saved"
	if kind == "onboarding-save" {
		v, err = onboardingRecord(s, u, t, d)
		if err != nil {
			return nil, err
		}
	} else {
		old := find(s["vendors"], str(d, "id"))
		if old == nil || str(old, "tenantId") != t || old["deleted"] == true {
			return nil, errors.New("Vendor not found")
		}
		v = M{}
		for k, val := range old {
			v[k] = val
		}
		action = str(d, "action")
		if action == "submit" {
			if !can(s, u, t, "submit", "vendor_onboarding") || str(old, "onboardingStatus") != "Draft" || (!admin(u) && str(old, "createdBy") != str(u, "id")) {
				return nil, errors.New("Only creator can submit an onboarding draft")
			}
			v["onboardingStatus"] = "Submitted"
		} else {
			if !admin(u) || str(old, "onboardingStatus") != "Submitted" {
				return nil, errors.New("Superuser must review a submitted vendor")
			}
			switch action {
			case "activate":
				v["onboardingStatus"] = "Active"
			case "return", "reject":
				if strings.TrimSpace(str(d, "reason")) == "" {
					return nil, errors.New("Give a reason")
				}
				v["onboardingStatus"] = "Rejected"
				if action == "return" {
					v["onboardingStatus"] = "Draft"
				}
			default:
				return nil, errors.New("Invalid onboarding action")
			}
		}
		v["updatedAt"] = now()
	}
	if find(s["vendors"], str(v, "id")) == nil {
		_, err = tx.Exec("INSERT INTO vendors(id,tenant_id,deleted,body) VALUES($1,$2,false,$3)", str(v, "id"), t, jsonBody(v))
	} else {
		_, err = tx.Exec("UPDATE vendors SET body=$1 WHERE id=$2 AND tenant_id=$3", jsonBody(v), str(v, "id"), t)
	}
	if err != nil {
		return nil, err
	}
	return M{"ok": true}, logVendorAudit(tx, u, t, "Vendor onboarding "+action+" "+str(d, "reason"), str(v, "name"), find(s["vendors"], str(v, "id")), v)
}

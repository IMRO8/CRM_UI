package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/mail"
	"regexp"
	"strings"
)

var vendorFields = []string{"name", "trade", "contactName", "phone", "email", "gstin", "address", "vendorType", "pan", "onboardingStatus", "notes"}

func generatedPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func vendorRecord(s State, t string, d M) (M, error) {
	var old M
	if str(d, "id") != "" {
		old = find(s["vendors"], str(d, "id"))
		if old == nil || str(old, "tenantId") != t || old["deleted"] == true {
			return nil, errors.New("Vendor not found in this project")
		}
	}
	v := M{"id": uuid(), "tenantId": t, "deleted": false, "updatedAt": now()}
	if old != nil {
		v["id"] = str(old, "id")
	}
	for _, key := range vendorFields {
		value := strings.TrimSpace(str(d, key))
		if len(value) > 1000 {
			return nil, errors.New("Vendor details exceed the allowed length")
		}
		v[key] = value
	}
	if str(v, "name") == "" {
		return nil, errors.New("Vendor name is required")
	}
	if str(v, "email") != "" {
		address, err := mail.ParseAddress(str(v, "email"))
		if err != nil || address.Address != str(v, "email") {
			return nil, errors.New("Enter a valid email address")
		}
	}
	v["onboardingStatus"] = "Active"
	if old != nil && str(old, "onboardingStatus") != "" {
		v["onboardingStatus"] = old["onboardingStatus"]
		v["createdBy"] = old["createdBy"]
		v["createdAt"] = old["createdAt"]
		v["data"] = old["data"]
		v["formVersion"] = old["formVersion"]
	}
	return v, nil
}
func redactVendor(s State, u M, t string, v M) M {
	if (!can(s, u, t, "view", "vendor") && !can(s, u, t, "view", "vendor_onboarding")) || (str(v, "onboardingStatus") != "" && str(v, "onboardingStatus") != "Active" && !can(s, u, t, "view", "vendor_onboarding")) {
		return nil
	}
	for key := range obj(v, "data") {
		if access(s, u, t, key, "vendor_onboarding") == "hidden" {
			delete(obj(v, "data"), key)
		}
	}
	for key := range obj(v, "vendorData") {
		if access(s, u, t, key, "vendor") == "hidden" {
			delete(obj(v, "vendorData"), key)
		}
	}
	for _, key := range vendorFields {
		if key != "onboardingStatus" && (!can(s, u, t, "view", "vendor") || access(s, u, t, key, "vendor") == "hidden") && (!can(s, u, t, "view", "vendor_onboarding") || access(s, u, t, key, "vendor_onboarding") == "hidden") {
			delete(v, key)
		}
	}
	return v
}
func manageVendorUser(tx *sql.Tx, s State, u M, t, kind string, d M) (M, error) {
	if !admin(u) {
		return nil, errors.New("Only the platform superuser can manage vendors and users")
	}
	switch kind {
	case "vendor":
		v, err := vendorRecord(s, t, d)
		if err != nil {
			return nil, err
		}
		before := find(s["vendors"], str(v, "id"))
		var schema M
		if before != nil && number(before, "vendorFormVersion") > 0 {
			schema = M{"formVersion": before["vendorFormVersion"], "data": before["vendorData"]}
		}
		data, version, e := financialExtras(s, u, t, d, "vendor", schema)
		if e != nil {
			return nil, e
		}
		v["vendorData"] = data
		v["vendorFormVersion"] = version
		if before == nil {
			_, err = tx.Exec("INSERT INTO vendors(id,tenant_id,deleted,body) VALUES($1,$2,false,$3)", str(v, "id"), t, jsonBody(v))
		} else {
			_, err = tx.Exec("UPDATE vendors SET body=$1 WHERE id=$2 AND tenant_id=$3", jsonBody(v), str(v, "id"), t)
		}
		if err != nil {
			return nil, err
		}
		action := "Vendor added"
		if before != nil {
			action = "Vendor details updated"
		}
		if err = logVendorAudit(tx, u, t, action, str(v, "name"), before, v); err != nil {
			return nil, err
		}
		return M{"ok": true}, nil
	case "vendor-delete":
		v := find(s["vendors"], str(d, "id"))
		if v == nil || str(v, "tenantId") != t || v["deleted"] == true {
			return nil, errors.New("Vendor not found in this project")
		}
		before := M{}
		for k, value := range v {
			before[k] = value
		}
		v["deleted"] = true
		v["deletedAt"] = now()
		_, err := tx.Exec("UPDATE vendors SET deleted=true,body=$1 WHERE id=$2 AND tenant_id=$3", jsonBody(v), str(v, "id"), t)
		if err != nil {
			return nil, err
		}
		return M{"ok": true}, logVendorAudit(tx, u, t, "Vendor removed from directory", str(v, "name"), before, v)
	case "user-create":
		name, username := strings.TrimSpace(str(d, "name")), strings.ToLower(strings.TrimSpace(str(d, "username")))
		if name == "" || len(name) > 200 {
			return nil, errors.New("Provide a full name up to 200 characters")
		}
		if !regexp.MustCompile(`^[a-z][a-z0-9._-]{2,63}$`).MatchString(username) {
			return nil, errors.New("Username must be 3–64 characters and start with a letter")
		}
		projects := obj(d, "projectRoles")
		for projectID, raw := range projects {
			if project := find(s["tenants"], projectID); project == nil || project["deleted"] == true {
				return nil, errors.New("Project not found")
			}
			list, valid := raw.([]any)
			if !valid {
				return nil, errors.New("Invalid project roles")
			}
			seen := map[string]bool{}
			for _, r := range list {
				rid, valid := r.(string)
				if !valid || find(s["roles"], rid) == nil || seen[rid] {
					return nil, errors.New("Select valid distinct roles")
				}
				seen[rid] = true
			}
		}
		password, err := generatedPassword()
		if err != nil {
			return nil, err
		}
		id := uuid()
		title := strings.TrimSpace(str(d, "title"))
		if title == "" {
			title = "Project user"
		}
		if len(title) > 200 {
			return nil, errors.New("Designation exceeds 200 characters")
		}
		_, err = tx.Exec("INSERT INTO users(id,username,name,title,superuser,password_hash) VALUES($1,$2,$3,$4,false,crypt($5,gen_salt('bf',12)))", id, username, name, title, password)
		if err != nil {
			return nil, err
		}
		for projectID, raw := range projects {
			roles := raw.([]any)
			if len(roles) == 0 {
				continue
			}
			if _, err = tx.Exec("INSERT INTO memberships(tenant_id,user_id) VALUES($1,$2)", projectID, id); err != nil {
				return nil, err
			}
			for _, roleID := range roles {
				if _, err = tx.Exec("INSERT INTO membership_roles(tenant_id,user_id,role_id) VALUES($1,$2,$3)", projectID, id, roleID); err != nil {
					return nil, err
				}
			}
			if err = logAudit(tx, u, projectID, "User project access assigned", username); err != nil {
				return nil, err
			}
		}
		if err = logAudit(tx, u, t, "User created", username); err != nil {
			return nil, err
		}
		return M{"ok": true, "credentials": M{"username": username, "password": password, "userId": id}}, nil
	case "user-credentials":
		target := find(s["users"], str(d, "id"))
		if target == nil || admin(target) {
			return nil, errors.New("Select a regular user account")
		}
		password, err := generatedPassword()
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec("UPDATE users SET password_hash=crypt($1,gen_salt('bf',12)) WHERE id=$2", password, str(target, "id")); err != nil {
			return nil, err
		}
		if _, err = tx.Exec("DELETE FROM sessions WHERE user_id=$1", str(target, "id")); err != nil {
			return nil, err
		}
		if err = logAudit(tx, u, t, "User credentials regenerated", str(target, "username")); err != nil {
			return nil, err
		}
		return M{"ok": true, "credentials": M{"username": str(target, "username"), "password": password, "userId": str(target, "id")}}, nil
	}
	return nil, errors.New("Unknown management operation")
}
func logVendorAudit(tx *sql.Tx, u M, t, action, target string, before, after M) error {
	m := M{"id": uuid(), "tenantId": t, "actorId": str(u, "id"), "action": action, "target": target, "at": now(), "before": before, "after": after}
	_, err := tx.Exec("INSERT INTO audit_log(id,tenant_id,actor_id,body) VALUES($1,$2,$3,$4)", str(m, "id"), t, str(u, "id"), jsonBody(m))
	return err
}

func redactVendorSnapshot(s State, u M, t string, v M) M {
	if !can(s, u, t, "view", "vendor") {
		return nil
	}
	for _, key := range vendorFields {
		if access(s, u, t, key, "vendor") == "hidden" {
			delete(v, key)
		}
	}
	delete(v, "data")
	for k := range obj(v, "vendorData") {
		if access(s, u, t, k, "vendor") == "hidden" {
			delete(obj(v, "vendorData"), k)
		}
	}
	return v
}

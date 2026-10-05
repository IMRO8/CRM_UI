package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var purchaseFields = []string{"amount", "description", "materials", "order_date"}
var inventoryFields = []string{"materials", "quantity"}

func integer(m M, k string, min, max int64) (int64, error) {
	v := m[k]
	var n int64
	var err error
	switch x := v.(type) {
	case json.Number:
		n, err = x.Int64()
	case float64:
		n = int64(x)
		if float64(n) != x {
			err = errors.New("fraction")
		}
	case int64:
		n = x
	case int:
		n = int64(x)
	default:
		err = errors.New("integer")
	}
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("Invalid %s", k)
	}
	return n, nil
}
func poStatus(s State, p M) string {
	return financialStatus(p, documentEvents(s["purchaseEvents"], "purchaseOrderId", str(p, "id")))
}

func stockBalance(s State, t, lot string) int64 {
	var n int64
	for _, m := range s["stockMovements"] {
		if str(m, "tenantId") == t && str(m, "lotId") == lot {
			n += number(m, "quantityMilli")
		}
	}
	return n
}
func fieldsEditable(s State, u M, t, f string, keys []string) bool {
	for _, k := range keys {
		if access(s, u, t, k, f) != "edit" {
			return false
		}
	}
	return true
}
func purchaseRecord(s State, u M, t string, d M) (M, error) {
	old := find(s["purchaseOrders"], str(d, "id"))
	if str(d, "id") != "" && (old == nil || str(old, "tenantId") != t) {
		return nil, errors.New("Purchase order not found")
	}
	action := "create"
	if old != nil {
		action = "edit"
		if poStatus(s, old) != "Draft" || (!admin(u) && str(old, "createdBy") != str(u, "id")) {
			return nil, errors.New("Only the creator or superuser can edit a draft")
		}
	}
	if !can(s, u, t, action, "purchase_order") || !fieldsEditable(s, u, t, "purchase_order", purchaseFields) {
		return nil, errors.New("Purchase order and field editing permissions are required")
	}
	v := find(s["vendors"], str(d, "vendorId"))
	if v == nil || str(v, "tenantId") != t || v["deleted"] == true || (str(v, "onboardingStatus") != "" && str(v, "onboardingStatus") != "Active") {
		return nil, errors.New("Select an active project vendor")
	}
	n, description := strings.TrimSpace(str(d, "number")), strings.TrimSpace(str(d, "description"))
	if n == "" || description == "" || len(n) > 100 || len(description) > 2000 {
		return nil, errors.New("Provide a purchase order number and description")
	}
	for _, p := range s["purchaseOrders"] {
		if str(p, "tenantId") == t && str(p, "number") == n && (old == nil || str(p, "id") != str(old, "id")) {
			return nil, errors.New("Purchase order number already exists in this project")
		}
	}
	input := arr(d, "lines")
	if len(input) == 0 || len(input) > 100 {
		return nil, errors.New("Add 1–100 material lines")
	}
	lines := []any{}
	var total int64
	for _, raw := range input {
		l, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("Invalid material line")
		}
		q, err := integer(l, "quantityMilli", 1, 1000000000)
		if err != nil {
			return nil, err
		}
		rate, err := integer(l, "rate", 0, 100000000000)
		if err != nil {
			return nil, err
		}
		material, unit, kind := strings.TrimSpace(str(l, "material")), strings.TrimSpace(str(l, "unit")), str(l, "kind")
		if material == "" || unit == "" || len(material) > 500 || len(unit) > 50 || (kind != "purchase" && kind != "rental") {
			return nil, errors.New("Each line needs a material, unit and type")
		}
		value := (q/1000)*rate + ((q%1000)*rate+500)/1000
		total += value
		if total > 100000000000000 {
			return nil, errors.New("Purchase order exceeds supported range")
		}
		lines = append(lines, M{"id": uuid(), "material": material, "unit": unit, "kind": kind, "quantityMilli": q, "rate": rate, "total": value})
	}
	f := latest(s, "purchase_order")
	if old != nil {
		for _, version := range s["forms"] {
			if str(version, "id") == "purchase_order" && number(version, "version") == number(old, "formVersion") {
				f = version
			}
		}
	}
	if f == nil {
		return nil, errors.New("Purchase order form not found")
	}
	data := obj(d, "data")
	known := map[string]bool{}
	for _, raw := range arr(f, "fields") {
		field := raw.(map[string]any)
		k := str(field, "key")
		known[k] = true
		value := strings.TrimSpace(str(data, k))
		if field["required"] == true && value == "" {
			return nil, errors.New(str(field, "label") + " is required")
		}
		if value != "" && access(s, u, t, k, "purchase_order") != "edit" {
			return nil, errors.New("Custom field editing permission is required")
		}
		if value != "" && str(field, "type") == "number" {
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return nil, errors.New("Custom field must be a number")
			}
		}
		if value != "" && str(field, "type") == "date" {
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return nil, errors.New("Custom field must be a date")
			}
		}
	}
	for k := range data {
		if !known[k] {
			return nil, errors.New("Unknown custom field: " + k)
		}
	}
	orderDate := str(d, "orderDate")
	if orderDate == "" && old != nil {
		orderDate = str(old, "orderDate")
		if orderDate == "" && len(str(old, "createdAt")) >= 10 {
			orderDate = str(old, "createdAt")[:10]
		}
	}
	if orderDate == "" {
		orderDate = now()[:10]
	}
	if _, err := time.Parse("2006-01-02", orderDate); err != nil {
		return nil, errors.New("Enter a valid purchase order date")
	}
	p := M{"id": uuid(), "tenantId": t, "number": n, "description": description, "vendorId": str(v, "id"), "vendor": str(v, "name"), "vendorSnapshot": v, "lines": lines, "total": total, "data": data, "orderDate": orderDate, "formVersion": number(f, "version"), "createdBy": str(u, "id"), "createdAt": now(), "updatedAt": now(), "approvalPolicy": 2, "creatorSuperuser": admin(u)}
	if old != nil {
		p["id"] = str(old, "id")
		p["createdBy"] = str(old, "createdBy")
		p["createdAt"] = str(old, "createdAt")
		p["approvalPolicy"] = old["approvalPolicy"]
		p["creatorSuperuser"] = old["creatorSuperuser"]
	}
	return p, nil
}
func purchaseEvent(s State, u M, t string, d M) (M, error) {
	p := find(s["purchaseOrders"], str(d, "id"))
	if p == nil || str(p, "tenantId") != t {
		return nil, errors.New("Purchase order not found")
	}
	a := str(d, "action")
	if !can(s, u, t, a, "purchase_order") {
		return nil, errors.New("Your roles do not allow this action")
	}
	st := poStatus(s, p)
	stage := ""
	if a == "submit" {
		if st != "Draft" {
			return nil, errors.New("Only drafts can be submitted")
		}
		if !admin(u) && str(p, "createdBy") != str(u, "id") {
			return nil, errors.New("Only the creator can submit")
		}
	} else {
		if !admin(u) && str(p, "createdBy") == str(u, "id") {
			return nil, errors.New("Creator cannot review or approve own purchase order")
		}
		switch a {
		case "review":
			if st != "Review" {
				return nil, errors.New("Purchase order is not awaiting review")
			}
		case "approve":
			var err error
			stage, err = financialApprovalStage(s, u, t, p, documentEvents(s["purchaseEvents"], "purchaseOrderId", str(p, "id")), "purchase_order")
			if err != nil {
				return nil, err
			}
		case "reject":
			if st == "Draft" || st == "Approved" || st == "Rejected" {
				return nil, errors.New("Only pending submissions can be rejected")
			}
		default:
			return nil, errors.New("Unsupported action")
		}
	}
	return M{"id": uuid(), "purchaseOrderId": str(p, "id"), "action": a, "stage": stage, "actorId": str(u, "id"), "at": now(), "superuserApproval": admin(u) && a == "approve"}, nil
}
func stockMovement(tx *sql.Tx, m M) error {
	var transfer any
	if str(m, "transferId") != "" {
		transfer = str(m, "transferId")
	}
	_, err := tx.Exec("INSERT INTO stock_movements(id,lot_id,tenant_id,kind,quantity_milli,transfer_id,actor_id,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", str(m, "id"), str(m, "lotId"), str(m, "tenantId"), str(m, "kind"), number(m, "quantityMilli"), transfer, str(m, "actorId"), jsonBody(m))
	return err
}
func manageProcurement(tx *sql.Tx, s State, u M, t, kind string, d M) (M, error) {
	ok := M{"ok": true}
	switch kind {
	case "purchase-order":
		p, err := purchaseRecord(s, u, t, d)
		if err != nil {
			return nil, err
		}
		if find(s["purchaseOrders"], str(p, "id")) == nil {
			_, err = tx.Exec("INSERT INTO purchase_orders(id,tenant_id,number,total,vendor_id,form_version,created_by,body,order_date) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)", str(p, "id"), t, str(p, "number"), number(p, "total"), str(p, "vendorId"), number(p, "formVersion"), str(p, "createdBy"), jsonBody(p), str(p, "orderDate"))
		} else {
			_, err = tx.Exec("UPDATE purchase_orders SET number=$1,total=$2,vendor_id=$3,body=$4,order_date=$7 WHERE id=$5 AND tenant_id=$6", str(p, "number"), number(p, "total"), str(p, "vendorId"), jsonBody(p), str(p, "id"), t, str(p, "orderDate"))
		}
		if err != nil {
			return nil, err
		}
		return ok, logAudit(tx, u, t, "Purchase order draft saved", str(p, "number"))
	case "purchase-transition":
		e, err := purchaseEvent(s, u, t, d)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec("INSERT INTO purchase_events(id,purchase_order_id,action,stage,actor_id,body) VALUES($1,$2,$3,$4,$5,$6)", str(e, "id"), str(e, "purchaseOrderId"), str(e, "action"), str(e, "stage"), str(u, "id"), jsonBody(e))
		if err != nil {
			return nil, err
		}
		return ok, logAudit(tx, u, t, "Purchase order "+str(e, "action")+" "+str(e, "stage"), str(find(s["purchaseOrders"], str(d, "id")), "number"))
	case "purchase-receive":
		p := find(s["purchaseOrders"], str(d, "id"))
		if p == nil || str(p, "tenantId") != t || poStatus(s, p) != "Approved" {
			return nil, errors.New("Receive only an approved purchase order")
		}
		if !can(s, u, t, "view", "purchase_order") || !can(s, u, t, "create", "inventory") || !fieldsEditable(s, u, t, "inventory", inventoryFields) {
			return nil, errors.New("Inventory receipt permission is required")
		}
		var line M
		for _, raw := range arr(p, "lines") {
			l := raw.(map[string]any)
			if str(l, "id") == str(d, "lineId") && str(l, "kind") == "purchase" {
				line = l
			}
		}
		if line == nil {
			return nil, errors.New("Rental lines cannot enter owned inventory")
		}
		q, err := integer(d, "quantityMilli", 1, 1000000000)
		if err != nil {
			return nil, err
		}
		var lot M
		for _, l := range s["stockLots"] {
			if str(l, "lineId") == str(line, "id") {
				lot = l
			}
		}
		var received int64
		for _, m := range s["stockMovements"] {
			if str(m, "kind") == "receipt" && str(m, "lineId") == str(line, "id") {
				received += number(m, "quantityMilli")
			}
		}
		if received+q > number(line, "quantityMilli") {
			return nil, errors.New("Receipt exceeds remaining ordered quantity")
		}
		if lot == nil {
			lot = M{"id": uuid(), "lineId": str(line, "id"), "purchaseOrderId": str(p, "id"), "originTenantId": t, "material": str(line, "material"), "unit": str(line, "unit"), "createdAt": now()}
			if _, err = tx.Exec("INSERT INTO stock_lots(id,purchase_order_id,origin_tenant_id,line_id,body) VALUES($1,$2,$3,$4,$5)", str(lot, "id"), str(p, "id"), t, str(line, "id"), jsonBody(lot)); err != nil {
				return nil, err
			}
		}
		m := M{"id": uuid(), "lotId": str(lot, "id"), "lineId": str(line, "id"), "tenantId": t, "kind": "receipt", "quantityMilli": q, "actorId": str(u, "id"), "at": now()}
		if err = stockMovement(tx, m); err != nil {
			return nil, err
		}
		return ok, logAudit(tx, u, t, "Purchased materials received into inventory", str(p, "number")+" · "+str(line, "material"))
	case "inventory-transfer", "inventory-issue":
		if !can(s, u, t, "edit", "inventory") || !fieldsEditable(s, u, t, "inventory", inventoryFields) {
			return nil, errors.New("Inventory editing permission is required")
		}
		lot := find(s["stockLots"], str(d, "lotId"))
		q, err := integer(d, "quantityMilli", 1, 1000000000)
		if err != nil {
			return nil, err
		}
		if lot == nil || q > stockBalance(s, t, str(lot, "id")) {
			return nil, errors.New("Quantity exceeds available project stock")
		}
		reason := strings.TrimSpace(str(d, "reason"))
		if reason == "" || len(reason) > 2000 {
			return nil, errors.New("A reason is required (up to 2000 characters)")
		}
		target := str(d, "targetTenantId")
		ref := ""
		movementKind := "issue"
		if kind == "inventory-transfer" {
			p := find(s["tenants"], target)
			if p == nil || p["deleted"] == true || target == t || !can(s, u, target, "create", "inventory") || !fieldsEditable(s, u, target, "inventory", inventoryFields) {
				return nil, errors.New("Select another active project where you can receive inventory")
			}
			ref = uuid()
			movementKind = "transfer-out"
			in := M{"id": uuid(), "lotId": str(lot, "id"), "tenantId": target, "kind": "transfer-in", "quantityMilli": q, "transferId": ref, "counterpartyId": t, "reason": reason, "actorId": str(u, "id"), "at": now()}
			if err = stockMovement(tx, in); err != nil {
				return nil, err
			}
			if err = logAudit(tx, u, target, "Inventory transfer received", str(lot, "material")); err != nil {
				return nil, err
			}
		}
		out := M{"id": uuid(), "lotId": str(lot, "id"), "tenantId": t, "kind": movementKind, "quantityMilli": -q, "transferId": ref, "counterpartyId": target, "reason": reason, "actorId": str(u, "id"), "at": now()}
		if err = stockMovement(tx, out); err != nil {
			return nil, err
		}
		return ok, logAudit(tx, u, t, "Inventory "+movementKind, str(lot, "material"))
	}
	return nil, errors.New("Unknown procurement operation")
}
func manageProject(tx *sql.Tx, s State, u M, kind string, d M) (M, error) {
	if !admin(u) {
		return nil, errors.New("Only the platform superuser can manage projects")
	}
	old := find(s["tenants"], str(d, "id"))
	if str(d, "id") != "" && (old == nil || old["deleted"] == true) {
		return nil, errors.New("Project not found")
	}
	if kind == "tenant-delete" {
		if old == nil {
			return nil, errors.New("Project not found")
		}
		for _, lot := range s["stockLots"] {
			if stockBalance(s, str(old, "id"), str(lot, "id")) > 0 {
				return nil, errors.New("Transfer or issue remaining inventory before deleting this project")
			}
		}
		old["deleted"] = true
		old["deletedAt"] = now()
		if _, err := tx.Exec("UPDATE tenants SET deleted=true,body=$1 WHERE id=$2", jsonBody(old), str(old, "id")); err != nil {
			return nil, err
		}
		return M{"ok": true}, logAudit(tx, u, str(old, "id"), "Project deleted from active use", str(old, "name"))
	}
	name, code := strings.TrimSpace(str(d, "name")), strings.ToUpper(strings.TrimSpace(str(d, "code")))
	if name == "" || code == "" || len(name) > 200 || len(code) > 30 {
		return nil, errors.New("Project name and code are required (up to 200 / 30 characters)")
	}
	p := M{"id": uuid(), "name": name, "code": code, "location": strings.TrimSpace(str(d, "location")), "deleted": false}
	var err error
	action := "Project tenant created"
	if old != nil {
		p["id"] = str(old, "id")
		action = "Project updated"
		_, err = tx.Exec("UPDATE tenants SET code=$1,body=$2 WHERE id=$3", code, jsonBody(p), str(p, "id"))
	} else {
		_, err = tx.Exec("INSERT INTO tenants(id,code,body) VALUES($1,$2,$3)", str(p, "id"), code, jsonBody(p))
	}
	if err != nil {
		return nil, err
	}
	return M{"ok": true}, logAudit(tx, u, str(p, "id"), action, name)
}
func redactProcurement(s State, u M) {
	orders := []M{}
	visible := map[string]bool{}
	for _, p := range s["purchaseOrders"] {
		t := str(p, "tenantId")
		if !can(s, u, t, "view", "purchase_order") {
			continue
		}
		if access(s, u, t, "amount", "purchase_order") == "hidden" {
			delete(p, "total")
			for _, raw := range arr(p, "lines") {
				l := raw.(map[string]any)
				delete(l, "rate")
				delete(l, "total")
			}
		}
		if access(s, u, t, "order_date", "purchase_order") == "hidden" {
			delete(p, "orderDate")
		}
		if access(s, u, t, "description", "purchase_order") == "hidden" {
			delete(p, "description")
		}
		if access(s, u, t, "materials", "purchase_order") == "hidden" {
			p["lines"] = []any{}
		}
		for k := range obj(p, "data") {
			if access(s, u, t, k, "purchase_order") == "hidden" {
				delete(obj(p, "data"), k)
			}
		}
		if v := redactVendorSnapshot(s, u, t, obj(p, "vendorSnapshot")); v == nil {
			delete(p, "vendorSnapshot")
		} else {
			p["vendorSnapshot"] = v
		}
		orders = append(orders, p)
		visible[str(p, "id")] = true
	}
	s["purchaseOrders"] = orders
	ev := []M{}
	for _, e := range s["purchaseEvents"] {
		if visible[str(e, "purchaseOrderId")] {
			ev = append(ev, e)
		}
	}
	s["purchaseEvents"] = ev
	moves := []M{}
	lots := map[string]bool{}
	materialVisible := map[string]bool{}
	for _, m := range s["stockMovements"] {
		t := str(m, "tenantId")
		if !can(s, u, t, "view", "inventory") {
			continue
		}
		if access(s, u, t, "quantity", "inventory") == "hidden" {
			delete(m, "quantityMilli")
		}
		moves = append(moves, m)
		lots[str(m, "lotId")] = true
		if access(s, u, t, "materials", "inventory") != "hidden" {
			materialVisible[str(m, "lotId")] = true
		}
	}
	s["stockMovements"] = moves
	records := []M{}
	for _, l := range s["stockLots"] {
		id := str(l, "id")
		if !lots[id] {
			continue
		}
		if !materialVisible[id] {
			delete(l, "material")
			delete(l, "unit")
		}
		if !admin(u) && len(assigned(s, u, str(l, "originTenantId"))) == 0 {
			delete(l, "originTenantId")
			delete(l, "lineId")
			delete(l, "purchaseOrderId")
		}
		records = append(records, l)
	}
	s["stockLots"] = records
}

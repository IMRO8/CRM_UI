package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var suiteFields = map[string][]string{"rfq": {"description", "reference"}, "quotation": {"amount", "description", "vendor", "terms"}, "award": {"reference", "reason", "amount"}, "vendor_feedback": {"vendor", "reference", "ratings", "comments", "dates"}, "project_plan": {"amount", "targets", "dates"}, "progress": {"reference", "progress", "dates", "comments"}, "sale": {"amount", "customer", "unit", "status", "dates"}, "collection": {"amount", "reference", "dates", "remarks"}}

func suiteForm(k string) string {
	switch k {
	case "rfq":
		return "quotation_request"
	case "quotation":
		return "vendor_quotation"
	case "award":
		return "quotation_selection"
	}
	return k
}
func suiteStatus(s State, d M) string {
	return financialStatus(d, documentEvents(s["suiteEvents"], "documentId", str(d, "id")))
}
func orderStatus(s State, o M) string {
	if o["workflowEnabled"] != true {
		return "Approved"
	}
	return financialStatus(o, documentEvents(s["orderEvents"], "orderId", str(o, "id")))
}
func suiteRecords(s State, k, t string) []M {
	out := []M{}
	for _, d := range s["suiteRecords"] {
		if str(d, "kind") == k && (t == "" || str(d, "tenantId") == t) {
			out = append(out, d)
		}
	}
	return out
}
func suiteRecord(s State, u M, t, k string, d M) (M, error) {
	keys, ok := suiteFields[k]
	if !ok {
		return nil, errors.New("Unknown record type")
	}
	f := suiteForm(k)
	old := find(s["suiteRecords"], str(d, "id"))
	action := "create"
	if str(d, "id") != "" {
		if old == nil || str(old, "tenantId") != t || str(old, "kind") != k {
			return nil, errors.New("Record not found")
		}
		action = "edit"
		if !admin(u) && str(old, "createdBy") != str(u, "id") {
			return nil, errors.New("Only creator or Superuser can edit")
		}
		if k == "award" || k == "progress" || k == "collection" || k == "project_plan" || k == "vendor_feedback" {
			return nil, errors.New("History is retained; add a new record or version")
		}
		for _, a := range suiteRecords(s, "award", t) {
			if str(a, "quotationId") == str(old, "id") || str(a, "rfqId") == str(old, "id") {
				return nil, errors.New("Selected quotations and packages are retained")
			}
		}
	}
	if !can(s, u, t, action, f) || !fieldsEditable(s, u, t, f, keys) {
		return nil, errors.New("Form and field editing permissions are required")
	}
	data, version, err := financialExtras(s, u, t, d, f, old)
	if err != nil {
		return nil, err
	}
	m := M{"id": uuid(), "kind": k, "tenantId": t, "createdBy": str(u, "id"), "createdAt": now(), "updatedAt": now(), "data": data, "formVersion": version}
	if old != nil {
		for _, key := range []string{"id", "createdBy", "createdAt"} {
			m[key] = old[key]
		}
	}
	var validation error
	text := func(key string) string {
		v := strings.TrimSpace(str(d, key))
		if v == "" || len(v) > 3000 {
			validation = fmt.Errorf("%s is required and must be at most 3000 characters", key)
		}
		m[key] = v
		return v
	}
	num := func(key string, min, max int64) int64 {
		v, e := integer(d, key, min, max)
		if e != nil {
			validation = e
		}
		m[key] = v
		return v
	}
	date := func(key string) string {
		v := text(key)
		if !financeDate(v) {
			validation = fmt.Errorf("%s needs a valid date", key)
		}
		return v
	}
	order := func(id string) M {
		o := find(s["orders"], id)
		if o == nil || str(o, "tenantId") != t || !can(s, u, t, "view", "work_order") {
			validation = errors.New("Select a work order in this project")
		}
		return o
	}
	vendor := func(id string) M {
		v := find(s["vendors"], id)
		if v == nil || str(v, "tenantId") != t || v["deleted"] == true || (str(v, "onboardingStatus") != "" && str(v, "onboardingStatus") != "Active") {
			validation = errors.New("Select an active project vendor")
		}
		return v
	}
	switch k {
	case "rfq":
		if old != nil {
			for _, q := range suiteRecords(s, "quotation", t) {
				if str(q, "rfqId") == str(old, "id") {
					return nil, errors.New("A work package with quotations is retained")
				}
			}
		}
		text("title")
		text("description")
		if !contains([]any{"Material construction", "Labour"}, text("contractType")) {
			return nil, errors.New("Choose a supported contract type")
		}
		m["referenceOrderId"] = str(d, "referenceOrderId")
		if str(m, "referenceOrderId") != "" {
			order(str(m, "referenceOrderId"))
		}
	case "quotation":
		r := find(s["suiteRecords"], str(d, "rfqId"))
		if r == nil || str(r, "kind") != "rfq" || str(r, "tenantId") != t || !can(s, u, t, "view", "quotation_request") {
			return nil, errors.New("Select a project quotation package")
		}
		for _, a := range suiteRecords(s, "award", t) {
			if str(a, "rfqId") == str(r, "id") && suiteStatus(s, a) != "Rejected" {
				return nil, errors.New("This work package already has an active selection")
			}
		}
		v := vendor(str(d, "vendorId"))
		m["vendorId"] = str(v, "id")
		m["vendor"] = str(v, "name")
		m["rfqId"] = str(r, "id")
		n := text("number")
		for _, q := range suiteRecords(s, "quotation", t) {
			if str(q, "id") != str(m, "id") && str(q, "vendorId") == str(v, "id") && strings.EqualFold(str(q, "number"), n) {
				return nil, errors.New("Quotation number already exists for this vendor")
			}
		}
		date("quotationDate")
		num("amount", 1, 100000000000000)
		text("description")
		num("deliveryDays", 1, 3650)
		num("creditDays", 0, 3650)
		text("terms")
	case "award":
		q := find(s["suiteRecords"], str(d, "quotationId"))
		if q == nil || str(q, "kind") != "quotation" || str(q, "tenantId") != t || !can(s, u, t, "view", "vendor_quotation") {
			return nil, errors.New("Select a project quotation")
		}
		for _, a := range suiteRecords(s, "award", t) {
			if str(a, "rfqId") == str(q, "rfqId") && suiteStatus(s, a) != "Rejected" {
				return nil, errors.New("Only one active selection per package")
			}
		}
		m["quotationId"] = str(q, "id")
		m["rfqId"] = str(q, "rfqId")
		m["amount"] = number(q, "amount")
		m["approvalPolicy"] = 2
		m["creatorSuperuser"] = admin(u)
		text("reason")
	case "vendor_feedback":
		v := vendor(str(d, "vendorId"))
		m["vendorId"] = str(v, "id")
		m["vendor"] = str(v, "name")
		m["orderId"] = str(d, "orderId")
		if str(m, "orderId") != "" && str(order(str(m, "orderId")), "vendorId") != str(v, "id") {
			return nil, errors.New("Work order must belong to this vendor")
		}
		for _, key := range []string{"quality", "timeliness", "safety", "communication"} {
			num(key, 1, 5)
		}
		text("comments")
		date("date")
	case "project_plan":
		num("budget", 1, 100000000000000)
		num("salesTarget", 0, 100000000000000)
		num("unitTarget", 0, 1000000)
		date("startDate")
		date("endDate")
		if str(m, "endDate") < str(m, "startDate") {
			return nil, errors.New("Project end precedes start")
		}
		targets := []any{}
		seen := map[string]bool{}
		for _, raw := range arr(d, "monthlyTargets") {
			x, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("Invalid target")
			}
			month := str(x, "month")
			if !financeDate(month+"-01") || seen[month] {
				return nil, errors.New("Target months must be valid and unique")
			}
			seen[month] = true
			cash, e := integer(x, "cash", 0, 100000000000000)
			if e != nil {
				return nil, e
			}
			progress, e := integer(x, "progress", 0, 100)
			if e != nil {
				return nil, e
			}
			targets = append(targets, M{"month": month, "cash": cash, "progress": progress})
		}
		m["monthlyTargets"] = targets
	case "progress":
		o := order(str(d, "orderId"))
		if orderStatus(s, o) != "Approved" {
			return nil, errors.New("Progress requires an approved work order")
		}
		m["orderId"] = str(o, "id")
		num("percent", 0, 100)
		date("date")
		text("comments")
	case "sale":
		if old != nil {
			for _, sch := range extRows(s, "payment_schedule", t) {
				if str(sch, "saleId") == str(old, "id") && (number(d, "amount") != number(old, "amount") || str(d, "unit") != str(old, "unit") || str(d, "date") != str(old, "date") || str(d, "status") != str(old, "status")) {
					return nil, errors.New("Scheduled sale value and unit retained")
				}
			}
		}
		unit := text("unit")
		text("customer")
		st := text("status")
		if !contains([]any{"Lead", "Reserved", "Booked", "Cancelled"}, st) {
			return nil, errors.New("Invalid sales stage")
		}
		n := num("amount", 1, 100000000000000)
		date("date")
		for _, sale := range suiteRecords(s, "sale", t) {
			if str(sale, "id") != str(m, "id") && strings.EqualFold(str(sale, "unit"), unit) && str(sale, "status") != "Cancelled" && st != "Cancelled" {
				return nil, errors.New("Unit already has an active sales record")
			}
		}
		var received int64
		for _, r := range suiteRecords(s, "collection", t) {
			if str(r, "saleId") == str(m, "id") {
				received += number(r, "amount") + number(r, "tds")
			}
		}
		if received > 0 && (st != "Booked" || n < received) {
			return nil, errors.New("Collected sales cannot be cancelled or reduced below receipts")
		}
	case "collection":
		sale := find(s["suiteRecords"], str(d, "saleId"))
		if sale == nil || str(sale, "kind") != "sale" || str(sale, "tenantId") != t || str(sale, "status") != "Booked" || !can(s, u, t, "view", "sale") {
			return nil, errors.New("Select a booked project sale")
		}
		m["saleId"] = str(sale, "id")
		n := num("amount", 1, 100000000000000)
		if _, ok := d["tds"]; !ok {
			d["tds"] = 0
		}
		tds := num("tds", 0, 100000000000000)
		m["mode"] = str(d, "mode")
		m["bank"] = str(d, "bank")
		dt := date("date")
		ref := text("reference")
		m["remarks"] = strings.TrimSpace(str(d, "remarks"))
		var received int64
		for _, r := range suiteRecords(s, "collection", t) {
			if str(r, "saleId") == str(sale, "id") {
				received += number(r, "amount") + number(r, "tds")
			}
			if strings.EqualFold(str(r, "reference"), ref) {
				return nil, errors.New("Receipt reference already exists")
			}
		}
		if received+n+tds > number(sale, "amount") {
			return nil, errors.New("Collections cannot exceed booked value")
		}
		if dt < str(sale, "date") {
			return nil, errors.New("Receipt date precedes sale")
		}
	}
	return m, validation
}
func suiteEvent(s State, u M, t string, d M, isOrder bool) (M, error) {
	list := s["suiteRecords"]
	key, f := "documentId", "quotation_selection"
	events := s["suiteEvents"]
	if isOrder {
		list = s["orders"]
		key = "orderId"
		f = "work_order"
		events = s["orderEvents"]
	}
	doc := find(list, str(d, "id"))
	if doc == nil || str(doc, "tenantId") != t || (!isOrder && str(doc, "kind") != "award") || (isOrder && doc["workflowEnabled"] != true) {
		return nil, errors.New("Approval record not found")
	}
	a := str(d, "action")
	ev := documentEvents(events, key, str(doc, "id"))
	st := financialStatus(doc, ev)
	stage := ""
	if !can(s, u, t, a, f) {
		return nil, errors.New("Approval action permission is required")
	}
	if a == "submit" {
		if st != "Draft" || (!admin(u) && str(doc, "createdBy") != str(u, "id")) {
			return nil, errors.New("Only creator or Superuser can submit a draft")
		}
	} else {
		if !admin(u) && str(doc, "createdBy") == str(u, "id") {
			return nil, errors.New("Creator cannot review or approve own submission")
		}
		switch a {
		case "review":
			if st != "Review" {
				return nil, errors.New("Review is not pending")
			}
		case "reject":
			if st == "Draft" || st == "Approved" || st == "Rejected" {
				return nil, errors.New("Only pending submissions can be rejected")
			}
		case "approve":
			var e error
			stage, e = financialApprovalStage(s, u, t, doc, ev, f)
			if e != nil {
				return nil, e
			}
		default:
			return nil, errors.New("Unsupported action")
		}
	}
	if isOrder && str(doc, "contractType") == "Labour" && a == "approve" {
		found := false
		for _, p := range s["purchaseOrders"] {
			if str(p, "orderId") == str(doc, "id") && poStatus(s, p) == "Approved" {
				found = true
			}
		}
		if !found {
			return nil, errors.New("Approve a linked labour purchase order first")
		}
	}
	return M{"id": uuid(), key: str(doc, "id"), "action": a, "stage": stage, "actorId": str(u, "id"), "at": now()}, nil
}
func workContract(s State, u M, t string, d M) (M, error) {
	if !fieldsEditable(s, u, t, "work_order", []string{"quotation", "contract"}) {
		return nil, errors.New("Quotation and contract editing permissions are required")
	}
	q := find(s["suiteRecords"], str(d, "quotationId"))
	if q == nil || str(q, "kind") != "quotation" || str(q, "tenantId") != t || !can(s, u, t, "view", "vendor_quotation") {
		return nil, errors.New("Approved quotation ID is mandatory")
	}
	approved := false
	for _, a := range suiteRecords(s, "award", t) {
		if str(a, "quotationId") == str(q, "id") && suiteStatus(s, a) == "Approved" {
			approved = true
		}
	}
	r := find(s["suiteRecords"], str(q, "rfqId"))
	if !approved || r == nil || str(q, "vendorId") != str(d, "vendorId") {
		return nil, errors.New("Use the approved selected quotation and its vendor")
	}
	for _, o := range s["orders"] {
		if str(o, "quotationId") == str(q, "id") {
			return nil, errors.New("Selected quotation is already used")
		}
	}
	if number(d, "base") > number(q, "amount") {
		return nil, errors.New("Work order cannot exceed its selected quotation")
	}
	ct := str(r, "contractType")
	m := M{"quotationId": str(q, "id"), "quotationNumber": str(q, "number"), "rfqId": str(r, "id"), "contractType": ct, "workflowEnabled": true, "approvalPolicy": 2, "creatorSuperuser": admin(u), "orderDate": str(d, "orderDate")}
	if str(m, "orderDate") == "" {
		m["orderDate"] = now()[:10]
	}
	if !financeDate(str(m, "orderDate")) {
		return nil, errors.New("Enter valid work-order date")
	}
	if ct == "Labour" && d["boq"] != nil {
		return nil, errors.New("Labour contracts use approved purchase-order links")
	}
	if ct == "Material construction" {
		b := obj(d, "boq")
		raw, e := boqBytes(b)
		if e != nil {
			return nil, e
		}
		m["boq"] = M{"name": str(b, "name"), "mime": http.DetectContentType(raw), "signed": true, "size": len(raw), "sha256": hash(string(raw))}
	}
	return m, nil
}
func boqBytes(b M) ([]byte, error) {
	raw, e := base64.StdEncoding.DecodeString(str(b, "base64"))
	if e != nil || len(raw) == 0 || len(raw) > 1048576 || b["signed"] != true || str(b, "name") == "" || len(str(b, "name")) > 180 {
		return nil, errors.New("Attach a signed BOQ PDF or image up to 1 MB")
	}
	mime := http.DetectContentType(raw)
	if mime != "application/pdf" && mime != "image/png" && mime != "image/jpeg" && !bytes.HasPrefix(raw, []byte("%PDF-")) {
		return nil, errors.New("BOQ must be a PDF, PNG or JPEG")
	}
	return raw, nil
}
func manageSuite(tx *sql.Tx, s State, u M, t, kind string, d M) (M, error) {
	if kind == "suite-save" {
		m, e := suiteRecord(s, u, t, str(d, "recordKind"), d)
		if e != nil {
			return nil, e
		}
		if find(s["suiteRecords"], str(m, "id")) == nil {
			_, e = tx.Exec("INSERT INTO operational_records(id,tenant_id,kind,form_id,form_version,created_by,body) VALUES($1,$2,$3,$4,$5,$6,$7)", str(m, "id"), t, str(m, "kind"), suiteForm(str(m, "kind")), number(m, "formVersion"), str(m, "createdBy"), jsonBody(m))
		} else {
			_, e = tx.Exec("UPDATE operational_records SET body=$1 WHERE id=$2 AND tenant_id=$3", jsonBody(m), str(m, "id"), t)
		}
		if e != nil {
			return nil, e
		}
		return M{"ok": true}, logVendorAudit(tx, u, t, str(m, "kind")+" saved", str(m, "id"), find(s["suiteRecords"], str(m, "id")), m)
	}
	isOrder := kind == "order-transition"
	ev, e := suiteEvent(s, u, t, d, isOrder)
	if e != nil {
		return nil, e
	}
	table, key := "operational_events", "document_id"
	idkey := "documentId"
	if isOrder {
		table = "work_order_events"
		key = "order_id"
		idkey = "orderId"
	}
	_, e = tx.Exec("INSERT INTO "+table+"(id,"+key+",action,stage,actor_id,body) VALUES($1,$2,$3,$4,$5,$6)", str(ev, "id"), str(ev, idkey), str(ev, "action"), str(ev, "stage"), str(u, "id"), jsonBody(ev))
	if e != nil {
		return nil, e
	}
	return M{"ok": true}, logAudit(tx, u, t, kind+" "+str(ev, "action"), str(ev, idkey))
}
func redactSuite(s State, u M) {
	mask := map[string][]string{"amount": {"amount", "tds", "budget", "salesTarget", "monthlyTargets"}, "description": {"title", "description"}, "reference": {"referenceOrderId", "quotationId", "rfqId", "orderId", "saleId", "reference"}, "reason": {"reason"}, "vendor": {"vendorId", "vendor"}, "terms": {"terms", "deliveryDays", "creditDays"}, "ratings": {"quality", "experience", "timeliness", "safety", "communication"}, "comments": {"comments"}, "dates": {"date", "quotationDate", "startDate", "endDate"}, "targets": {"unitTarget", "monthlyTargets"}, "progress": {"percent"}, "customer": {"customer"}, "unit": {"unit"}, "status": {"status"}, "weights": {"weights"}, "remarks": {"remarks"}}
	out := []M{}
	visible := map[string]bool{}
	for _, d := range s["suiteRecords"] {
		t, f := str(d, "tenantId"), suiteForm(str(d, "kind"))
		if !can(s, u, t, "view", f) {
			continue
		}
		for field, keys := range mask {
			if access(s, u, t, field, f) == "hidden" {
				for _, key := range keys {
					delete(d, key)
				}
			}
		}
		for key := range obj(d, "data") {
			if access(s, u, t, key, f) == "hidden" {
				delete(obj(d, "data"), key)
			}
		}
		out = append(out, d)
		visible[str(d, "id")] = true
	}
	s["suiteRecords"] = out
	ev := []M{}
	for _, e := range s["suiteEvents"] {
		if visible[str(e, "documentId")] {
			ev = append(ev, e)
		}
	}
	s["suiteEvents"] = ev
	ev = []M{}
	for _, e := range s["orderEvents"] {
		o := find(s["orders"], str(e, "orderId"))
		if o != nil && can(s, u, str(o, "tenantId"), "view", "work_order") {
			ev = append(ev, e)
		}
	}
	s["orderEvents"] = ev
}
func serveBOQ(w http.ResponseWriter, r *http.Request, tx *sql.Tx, id, user string) {
	s, e := loadState(tx)
	if e != nil {
		fail(w, e)
		return
	}
	o := find(s["orders"], id)
	u := find(s["users"], user)
	t := str(o, "tenantId")
	if o == nil || !can(s, u, t, "view", "work_order") || access(s, u, t, "contract", "work_order") == "hidden" || access(s, u, t, "amount", "work_order") == "hidden" {
		write(w, 403, M{"error": "BOQ access is restricted"})
		return
	}
	var raw []byte
	var name, mime string
	e = tx.QueryRow("SELECT content,name,mime FROM work_order_boq WHERE order_id=$1", id).Scan(&raw, &name, &mime)
	if e != nil {
		write(w, 404, M{"error": "BOQ not found"})
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+strings.NewReplacer("\"", "", "\r", "", "\n", "", "\\", "_").Replace(name)+"\"")
	w.Write(raw)
}

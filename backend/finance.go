package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var financeFields = []string{"amount", "description", "category", "dates", "remarks", "reference"}

func financeStatus(s State, d M) string {
	return financialStatus(d, documentEvents(s["financeEvents"], "documentId", str(d, "id")))
}
func invoiceSettled(s State, id string) int64 {
	var n int64
	for _, p := range s["payments"] {
		if str(p, "invoiceId") == id && financeStatus(s, p) == "Approved" {
			n += number(p, "paid") + number(p, "tds")
		}
	}
	return n
}
func invoiceBalance(s State, i M) int64 { return number(i, "amount") - invoiceSettled(s, str(i, "id")) }
func invoiceStatus(s State, i M) string {
	st := "Open"
	for _, e := range s["invoiceEvents"] {
		if str(e, "invoiceId") == str(i, "id") {
			st = str(e, "status")
		}
	}
	return st
}
func canCost(s State, u M, t string) bool {
	eligible := admin(u)
	for _, r := range assigned(s, u, t) {
		if str(r, "stage") == "admin" {
			eligible = true
		}
	}
	return eligible && can(s, u, t, "view", "cost_overview") && access(s, u, t, "amount", "cost_overview") != "hidden"
}
func costSummary(s State, u M) []M {
	out := []M{}
	for _, t := range s["tenants"] {
		id := str(t, "id")
		if t["deleted"] == true || !canCost(s, u, id) {
			continue
		}
		var wo, po, extra, recorded, paid, tds, bal int64
		categories := M{}
		for _, o := range s["orders"] {
			if str(o, "tenantId") == id {
				wo += effective(s, o)
			}
		}
		for _, p := range s["purchaseOrders"] {
			if str(p, "tenantId") == id && poStatus(s, p) == "Approved" {
				po += number(p, "total")
				for _, raw := range arr(p, "lines") {
					l := raw.(map[string]any)
					c := "Material purchase"
					if str(l, "kind") == "rental" {
						c = "Equipment rental"
					}
					categories[c] = number(categories, c) + number(l, "total")
				}
			}
		}
		for _, d := range s["expenses"] {
			if str(d, "tenantId") == id && financeStatus(s, d) == "Approved" {
				recorded += number(d, "amount")
				if str(d, "referenceId") == "" {
					extra += number(d, "amount")
					c := str(d, "category")
					categories[c] = number(categories, c) + number(d, "amount")
				}
			}
		}
		for _, p := range s["payments"] {
			if str(p, "tenantId") == id && financeStatus(s, p) == "Approved" {
				paid += number(p, "paid")
				tds += number(p, "tds")
			}
		}
		for _, p := range extRows(s, "corporate_payment", id) {
			if extensionStatus(s, p) == "Approved" {
				paid += number(p, "paid")
				tds += number(p, "tds")
			}
		}
		for _, i := range s["invoices"] {
			if str(i, "tenantId") == id {
				bal += invoiceBalance(s, i)
			}
		}
		out = append(out, M{"tenantId": id, "project": str(t, "name"), "workOrders": wo, "purchases": po, "additional": extra, "total": wo + po + extra, "recordedExpenses": recorded, "paid": paid, "tds": tds, "outstanding": bal, "categories": categories})
	}
	return out
}
func redactFinance(s State, u M) {
	s["costs"] = costSummary(s, u)
	for _, i := range s["invoices"] {
		i["balance"] = invoiceBalance(s, i)
	}
	visible := map[string]bool{}
	invoices := map[string]bool{}
	for key, form := range map[string]string{"expenses": "expense", "payments": "payment", "invoices": "payment"} {
		out := []M{}
		for _, d := range s[key] {
			t := str(d, "tenantId")
			if !can(s, u, t, "view", form) {
				continue
			}
			if access(s, u, t, "amount", form) == "hidden" {
				for _, k := range []string{"amount", "paid", "tds", "balance"} {
					delete(d, k)
				}
			}
			for _, k := range []string{"description", "category", "remarks"} {
				if access(s, u, t, k, form) == "hidden" {
					delete(d, k)
				}
			}
			if access(s, u, t, "dates", form) == "hidden" {
				for _, k := range []string{"expenseDate", "invoiceDate", "paidDate", "orderDate"} {
					delete(d, k)
				}
			}
			if access(s, u, t, "reference", form) == "hidden" {
				for _, k := range []string{"referenceId", "referenceType", "orderId", "orderNumber", "vendorId", "vendor", "invoiceNumber", "invoiceId", "payee"} {
					delete(d, k)
				}
			}
			for k := range obj(d, "data") {
				if access(s, u, t, k, form) == "hidden" {
					delete(obj(d, "data"), k)
				}
			}
			out = append(out, d)
			if key == "invoices" {
				invoices[str(d, "id")] = true
			} else {
				visible[str(d, "id")] = true
			}
		}
		s[key] = out
	}
	events := []M{}
	for _, e := range s["financeEvents"] {
		if visible[str(e, "documentId")] {
			events = append(events, e)
		}
	}
	s["financeEvents"] = events
	events = []M{}
	for _, e := range s["invoiceEvents"] {
		if invoices[str(e, "invoiceId")] {
			events = append(events, e)
		}
	}
	s["invoiceEvents"] = events
}
func financeDate(v string) bool {
	d, e := time.Parse("2006-01-02", v)
	return e == nil && d.Format("2006-01-02") == v
}
func financialExtras(s State, u M, t string, d M, f string, old M) (M, int64, error) {
	version := latest(s, f)
	if old != nil {
		for _, v := range s["forms"] {
			if str(v, "id") == f && number(v, "version") == number(old, "formVersion") {
				version = v
			}
		}
	}
	if version == nil {
		return nil, 0, errors.New("Financial template not found")
	}
	data := obj(d, "data")
	known := map[string]bool{}
	for _, raw := range arr(version, "fields") {
		field := raw.(map[string]any)
		k := str(field, "key")
		known[k] = true
		if old != nil && access(s, u, t, k, f) != "edit" {
			if value, exists := data[k]; exists && fmt.Sprint(value) != fmt.Sprint(obj(old, "data")[k]) {
				return nil, 0, errors.New("Custom field editing permission is required")
			}
			if value, exists := obj(old, "data")[k]; exists {
				data[k] = value
			}
		}
		value := ""
		if data[k] != nil {
			value = strings.TrimSpace(fmt.Sprint(data[k]))
		}
		if field["required"] == true && value == "" {
			return nil, 0, errors.New(str(field, "label") + " is required")
		}
		if value != "" && old == nil && access(s, u, t, k, f) != "edit" {
			return nil, 0, errors.New("Custom field editing permission is required")
		}
		if value != "" && str(field, "type") == "date" && !financeDate(value) {
			return nil, 0, errors.New("Custom field needs a valid date")
		}
		if value != "" && str(field, "type") == "number" {
			if n, e := strconv.ParseFloat(value, 64); e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, 0, errors.New("Custom field needs a number")
			}
		}
	}
	for k := range data {
		if !known[k] {
			return nil, 0, errors.New("Unknown custom field")
		}
	}
	return data, number(version, "version"), nil
}
func financialRecord(s State, u M, t, kind string, d M) (M, error) {
	list := s["expenses"]
	if kind == "payment" {
		list = s["payments"]
	}
	old := find(list, str(d, "id"))
	if str(d, "id") != "" && (old == nil || str(old, "tenantId") != t) {
		return nil, errors.New("Record not found in this project")
	}
	action := "create"
	if old != nil {
		action = "edit"
		if financeStatus(s, old) != "Draft" || (!admin(u) && str(old, "createdBy") != str(u, "id")) {
			return nil, errors.New("Only the creator or superuser can edit a draft")
		}
	}
	if !can(s, u, t, action, kind) || !fieldsEditable(s, u, t, kind, financeFields) {
		return nil, errors.New("Form and field editing permissions are required")
	}
	data, version, err := financialExtras(s, u, t, d, kind, old)
	if err != nil {
		return nil, err
	}
	m := M{"id": uuid(), "kind": kind, "tenantId": t, "createdBy": str(u, "id"), "createdAt": now(), "approvalPolicy": 2, "creatorSuperuser": admin(u), "data": data, "formVersion": version, "remarks": strings.TrimSpace(str(d, "remarks"))}
	if old != nil {
		for _, k := range []string{"id", "createdBy", "createdAt", "creatorSuperuser"} {
			m[k] = old[k]
		}
	}
	if kind == "expense" {
		n, err := integer(d, "amount", 1, 100000000000000)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(str(d, "description")) == "" || strings.TrimSpace(str(d, "category")) == "" || strings.TrimSpace(str(d, "payee")) == "" || !financeDate(str(d, "expenseDate")) {
			return nil, errors.New("Description, category, payee and a valid date are required")
		}
		refType, ref := str(d, "referenceType"), str(d, "referenceId")
		if ref != "" {
			list := s["orders"]
			if refType == "purchase_order" {
				list = s["purchaseOrders"]
			} else if refType != "work_order" {
				return nil, errors.New("Invalid order reference")
			}
			r := find(list, ref)
			if r == nil || str(r, "tenantId") != t || !can(s, u, t, "view", refType) || (refType == "work_order" && orderStatus(s, r) != "Approved") || (refType == "purchase_order" && poStatus(s, r) != "Approved") {
				return nil, errors.New("Select a valid order from this project")
			}
		} else {
			refType = ""
		}
		m["amount"] = n
		m["referenceId"] = ref
		m["referenceType"] = refType
		for _, k := range []string{"description", "category", "payee", "expenseDate"} {
			m[k] = strings.TrimSpace(str(d, k))
		}
	} else {
		i := find(s["invoices"], str(d, "invoiceId"))
		if i == nil || str(i, "tenantId") != t || invoiceStatus(s, i) != "Open" {
			return nil, errors.New("Select an open project invoice")
		}
		paid, e := integer(d, "paid", 0, 100000000000000)
		if e != nil {
			return nil, e
		}
		tds, e := integer(d, "tds", 0, 100000000000000)
		if e != nil {
			return nil, e
		}
		if paid+tds <= 0 || paid+tds > invoiceBalance(s, i) {
			return nil, errors.New("Paid amount + TDS cannot exceed invoice balance")
		}
		date := str(d, "paidDate")
		if !financeDate(date) || date < str(i, "invoiceDate") {
			return nil, errors.New("Paid date must be valid and on or after invoice date")
		}
		m["amount"] = paid + tds
		m["paid"] = paid
		m["tds"] = tds
		m["invoiceId"] = str(i, "id")
		m["paidDate"] = date
	}
	return m, nil
}
func invoiceRecord(s State, u M, t string, d M) (M, error) {
	if !can(s, u, t, "create", "payment") || !fieldsEditable(s, u, t, "payment", financeFields) {
		return nil, errors.New("Invoice creation and field editing permissions are required")
	}
	o := find(s["orders"], str(d, "orderId"))
	v := find(s["vendors"], str(o, "vendorId"))
	if o == nil || str(o, "tenantId") != t || v == nil || v["deleted"] == true || (str(v, "onboardingStatus") != "" && str(v, "onboardingStatus") != "Active") || !can(s, u, t, "view", "work_order") {
		return nil, errors.New("Select a work order with an active project vendor")
	}
	n, e := integer(d, "amount", 1, 100000000000000)
	if e != nil {
		return nil, e
	}
	num := strings.TrimSpace(str(d, "invoiceNumber"))
	if num == "" || len(num) > 100 || strings.TrimSpace(str(d, "description")) == "" || strings.TrimSpace(str(d, "category")) == "" || !financeDate(str(d, "invoiceDate")) {
		return nil, errors.New("Invoice number, description, category and date are required")
	}
	sum := n
	for _, i := range s["invoices"] {
		if str(i, "tenantId") == t && str(i, "vendorId") == str(v, "id") && strings.EqualFold(str(i, "invoiceNumber"), num) {
			return nil, errors.New("This invoice number already exists for the vendor")
		}
		if str(i, "orderId") == str(o, "id") {
			sum += number(i, "amount")
		}
	}
	if sum > effective(s, o) {
		return nil, errors.New("Invoices cannot exceed the current work order value")
	}
	date := str(o, "orderDate")
	if date == "" && len(str(o, "createdAt")) >= 10 {
		date = str(o, "createdAt")[:10]
	}
	data, version, metaErr := financialExtras(s, u, t, d, "payment", nil)
	if metaErr != nil {
		return nil, metaErr
	}
	return M{"data": data, "formVersion": version, "id": uuid(), "tenantId": t, "orderId": str(o, "id"), "orderNumber": str(o, "number"), "orderDate": date, "vendorId": str(v, "id"), "vendor": str(v, "name"), "invoiceNumber": num, "invoiceDate": str(d, "invoiceDate"), "description": strings.TrimSpace(str(d, "description")), "category": strings.TrimSpace(str(d, "category")), "amount": n, "remarks": strings.TrimSpace(str(d, "remarks")), "createdBy": str(u, "id"), "createdAt": now()}, nil
}
func financialEvent(s State, u M, t string, d M) (M, error) {
	doc := find(s["expenses"], str(d, "id"))
	if doc == nil {
		doc = find(s["payments"], str(d, "id"))
	}
	if doc == nil || str(doc, "tenantId") != t {
		return nil, errors.New("Financial record not found")
	}
	kind, action := str(doc, "kind"), str(d, "action")
	if !can(s, u, t, action, kind) {
		return nil, errors.New("Financial action permission is required")
	}
	st := financeStatus(s, doc)
	stage := ""
	if action == "submit" {
		if st != "Draft" || (!admin(u) && str(doc, "createdBy") != str(u, "id")) {
			return nil, errors.New("Only the creator can submit a draft")
		}
	} else {
		if !admin(u) && str(doc, "createdBy") == str(u, "id") {
			return nil, errors.New("Creator cannot review or approve own record")
		}
		switch action {
		case "review":
			if st != "Review" {
				return nil, errors.New("Record is not awaiting review")
			}
		case "approve":
			var e error
			stage, e = financialApprovalStage(s, u, t, doc, documentEvents(s["financeEvents"], "documentId", str(doc, "id")), kind)
			if e != nil {
				return nil, e
			}
		case "reject":
			if st == "Draft" || st == "Approved" || st == "Rejected" {
				return nil, errors.New("Only pending submissions can be rejected")
			}
		default:
			return nil, errors.New("Unsupported action")
		}
	}
	if kind == "payment" && (action == "submit" || action == "approve") {
		i := find(s["invoices"], str(doc, "invoiceId"))
		if invoiceStatus(s, i) != "Open" || number(doc, "amount") > invoiceBalance(s, i) {
			return nil, errors.New("Approval blocked: invoice is closed or settlement exceeds balance")
		}
	}
	return M{"id": uuid(), "documentId": str(doc, "id"), "action": action, "stage": stage, "actorId": str(u, "id"), "at": now()}, nil
}
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func manageFinance(tx *sql.Tx, s State, u M, t, kind string, d M) (M, error) {
	ok := M{"ok": true}
	switch kind {
	case "expense", "payment":
		m, e := financialRecord(s, u, t, kind, d)
		if e != nil {
			return nil, e
		}
		var inv, date, pdate, wo, po any
		if kind == "payment" {
			inv = str(m, "invoiceId")
			pdate = str(m, "paidDate")
		} else {
			date = str(m, "expenseDate")
			if str(m, "referenceType") == "work_order" {
				wo = nullString(str(m, "referenceId"))
			}
			if str(m, "referenceType") == "purchase_order" {
				po = nullString(str(m, "referenceId"))
			}
		}
		list := s["expenses"]
		if kind == "payment" {
			list = s["payments"]
		}
		if find(list, str(m, "id")) == nil {
			_, e = tx.Exec("INSERT INTO finance_documents(id,kind,tenant_id,amount,paid,tds,invoice_id,expense_date,paid_date,ref_work_order,ref_purchase_order,form_version,created_by,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)", str(m, "id"), kind, t, number(m, "amount"), number(m, "paid"), number(m, "tds"), inv, date, pdate, wo, po, number(m, "formVersion"), str(m, "createdBy"), jsonBody(m))
		} else {
			_, e = tx.Exec("UPDATE finance_documents SET amount=$1,paid=$2,tds=$3,invoice_id=$4,expense_date=$5,paid_date=$6,ref_work_order=$7,ref_purchase_order=$8,body=$9 WHERE id=$10 AND tenant_id=$11", number(m, "amount"), number(m, "paid"), number(m, "tds"), inv, date, pdate, wo, po, jsonBody(m), str(m, "id"), t)
		}
		if e != nil {
			return nil, e
		}
		return ok, logAudit(tx, u, t, kind+" draft saved", str(m, "id"))
	case "invoice":
		m, e := invoiceRecord(s, u, t, d)
		if e != nil {
			return nil, e
		}
		_, e = tx.Exec("INSERT INTO vendor_invoices(id,tenant_id,order_id,vendor_id,invoice_number,invoice_date,amount,created_by,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)", str(m, "id"), t, str(m, "orderId"), str(m, "vendorId"), str(m, "invoiceNumber"), str(m, "invoiceDate"), number(m, "amount"), str(u, "id"), jsonBody(m))
		if e != nil {
			return nil, e
		}
		return ok, logAudit(tx, u, t, "Vendor invoice registered", str(m, "invoiceNumber"))
	case "finance-transition":
		e, err := financialEvent(s, u, t, d)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec("INSERT INTO finance_events(id,document_id,action,stage,actor_id,body) VALUES($1,$2,$3,$4,$5,$6)", str(e, "id"), str(e, "documentId"), str(e, "action"), str(e, "stage"), str(u, "id"), jsonBody(e))
		if err != nil {
			return nil, err
		}
		return ok, logAudit(tx, u, t, "Financial "+str(e, "action")+" "+str(e, "stage"), str(e, "documentId"))
	case "invoice-status":
		i := find(s["invoices"], str(d, "id"))
		if i == nil || str(i, "tenantId") != t || !can(s, u, t, "amend", "payment") {
			return nil, errors.New("Invoice amendment permission is required")
		}
		st, reason := str(d, "status"), strings.TrimSpace(str(d, "reason"))
		if !contains([]any{"Open", "Closed"}, st) || st == invoiceStatus(s, i) || reason == "" {
			return nil, errors.New("Choose a different status and give a reason")
		}
		if st == "Closed" && invoiceBalance(s, i) != 0 {
			return nil, errors.New("An outstanding invoice cannot be closed")
		}
		m := M{"id": uuid(), "invoiceId": str(i, "id"), "status": st, "reason": reason, "actorId": str(u, "id"), "at": now()}
		_, e := tx.Exec("INSERT INTO invoice_events(id,invoice_id,status,reason,actor_id,body) VALUES($1,$2,$3,$4,$5,$6)", str(m, "id"), str(i, "id"), st, reason, str(u, "id"), jsonBody(m))
		if e != nil {
			return nil, e
		}
		return ok, logAudit(tx, u, t, "Invoice status amended: "+st+" · "+reason, str(i, "invoiceNumber"))
	}
	return nil, errors.New("Unknown financial command")
}

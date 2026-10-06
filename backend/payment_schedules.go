package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"
)

var extensionFields = map[string][]string{"corporate_account": {"details"}, "corporate_payment": {"amount", "reference", "details", "dates"}, "booking": {"amount", "reference", "details", "dates"}, "schedule_template": {"details"}, "payment_schedule": {"amount", "reference", "details", "dates"}, "milestone": {"details", "dates"}, "demand": {"amount", "reference", "details", "dates"}, "receipt_allocation": {"amount", "reference"}}

func extRows(s State, k, t string) []M {
	out := []M{}
	for _, r := range s["extensions"] {
		if str(r, "kind") == k && (k == "corporate_account" || t == "" || str(r, "tenantId") == t) {
			out = append(out, r)
		}
	}
	return out
}
func extensionStatus(s State, d M) string {
	return financialStatus(d, documentEvents(s["extensionEvents"], "documentId", str(d, "id")))
}
func proportion(a, b int64) int64 { return a/10000*b + ((a%10000)*b+5000)/10000 }
func pricingTotals(p M) (M, error) {
	var base, other int64
	for _, raw := range arr(p, "partA") {
		l, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(str(l, "label")) == "" {
			return nil, errors.New("Labelled charges required")
		}
		a, e := integer(l, "amount", 0, 100000000000000)
		if e != nil {
			return nil, e
		}
		base += a
	}
	for _, raw := range arr(p, "partB") {
		l, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(str(l, "label")) == "" {
			return nil, errors.New("Labelled charges required")
		}
		a, e := integer(l, "amount", 0, 100000000000000)
		if e != nil {
			return nil, e
		}
		bps, e := integer(l, "gstBps", 0, 10000)
		if e != nil {
			return nil, e
		}
		other += a + proportion(a, bps)
	}
	g, e := integer(p, "gstBps", 0, 10000)
	if e != nil {
		return nil, e
	}
	td, e := integer(p, "tdsBps", 0, 10000)
	if e != nil {
		return nil, e
	}
	if base < 1 || base+other > 100000000000000 {
		return nil, errors.New("Pricing exceeds supported range")
	}
	gst := proportion(base, g)
	return M{"base": base, "gst": gst, "tds": proportion(base, td), "partB": other, "grandTotal": base + gst + other}, nil
}
func buildPaymentSchedule(b, template M) (M, error) {
	p := obj(b, "pricing")
	tot, e := pricingTotals(p)
	if e != nil {
		return nil, e
	}
	rows := []any{}
	var used, ug, ut, percent int64
	input := arr(template, "rows")
	for _, raw := range input {
		r := raw.(map[string]any)
		line := M{}
		for k, v := range r {
			line[k] = v
		}
		percent += number(r, "percent")
		cumulative := proportion(number(tot, "base"), percent*100)
		base := cumulative - used
		gst := proportion(cumulative, number(p, "gstBps")) - ug
		tds := proportion(cumulative, number(p, "tdsBps")) - ut
		used += base
		ug += gst
		ut += tds
		line["base"] = base
		line["gst"] = gst
		line["tds"] = tds
		line["gross"] = base + gst
		line["net"] = base + gst - tds
		rows = append(rows, line)
	}
	var bb int64
	for _, raw := range arr(p, "partB") {
		bb += number(raw.(map[string]any), "amount")
	}
	rows = append(rows, M{"code": "possession", "label": "Part B · possession charges", "trigger": "milestone", "delayDays": 0, "percent": nil, "base": bb, "gst": number(tot, "partB") - bb, "tds": 0, "gross": number(tot, "partB"), "net": number(tot, "partB")})
	return M{"rows": rows, "totals": tot}, nil
}
func scheduleDue(s State, schedule, row M) string {
	date := str(schedule, "bookingDate")
	if str(row, "trigger") == "milestone" {
		global, scoped := "", ""
		for _, m := range extRows(s, "milestone", str(schedule, "tenantId")) {
			if str(m, "code") == str(row, "code") {
				if str(m, "wing") == "" {
					global = str(m, "date")
				}
				if str(m, "wing") == str(schedule, "wing") {
					scoped = str(m, "date")
				}
			}
		}
		date = global
		if scoped != "" {
			date = scoped
		}
	}
	d, e := time.Parse("2006-01-02", date)
	if e != nil {
		return ""
	}
	return d.AddDate(0, 0, int(number(row, "delayDays"))).Format("2006-01-02")
}
func corporateBalance(s State, e M) int64 {
	balance := number(e, "amount")
	for _, p := range extRows(s, "corporate_payment", str(e, "tenantId")) {
		if str(p, "expenseId") == str(e, "id") && extensionStatus(s, p) == "Approved" {
			balance -= number(p, "paid") + number(p, "tds")
		}
	}
	return balance
}
func demandBalance(s State, d M) int64 {
	balance := number(d, "gross")
	for _, a := range extRows(s, "receipt_allocation", str(d, "tenantId")) {
		if str(a, "demandId") == str(d, "id") {
			balance -= number(a, "amount")
		}
	}
	return balance
}
func extensionRecord(s State, u M, t, k string, d M) (M, error) {
	keys, known := extensionFields[k]
	if !known {
		return nil, errors.New("Unknown form")
	}
	old := find(extRows(s, k, ""), str(d, "id"))
	action := "create"
	if str(d, "id") != "" {
		if old == nil || (k != "corporate_account" && str(old, "tenantId") != t) {
			return nil, errors.New("Record not found")
		}
		action = "edit"
	}
	if !can(s, u, t, action, k) || !fieldsEditable(s, u, t, k, keys) {
		return nil, errors.New("Form and field editing permission required")
	}
	if (k == "corporate_account" || k == "schedule_template") && !admin(u) {
		return nil, errors.New("Only Superuser can configure accounts and templates")
	}
	if old != nil && k != "corporate_account" && k != "booking" && k != "corporate_payment" {
		return nil, errors.New("History retained; add a new version")
	}
	if old != nil && k == "corporate_payment" && (extensionStatus(s, old) != "Draft" || (!admin(u) && str(old, "createdBy") != str(u, "id"))) {
		return nil, errors.New("Only creator or Superuser can edit a draft")
	}
	data, ver, e := financialExtras(s, u, t, d, k, old)
	if e != nil {
		return nil, e
	}
	m := M{"id": uuid(), "kind": k, "tenantId": t, "createdBy": str(u, "id"), "createdAt": now(), "updatedAt": now(), "data": data, "formVersion": ver}
	if old != nil {
		for _, key := range []string{"id", "createdBy", "createdAt"} {
			m[key] = old[key]
		}
	}
	var invalid error
	text := func(key, label string) string {
		v := strings.TrimSpace(str(d, key))
		if v == "" || len(v) > 3000 {
			invalid = errors.New(label + " is required (up to 3000 characters)")
		}
		m[key] = v
		return v
	}
	num := func(key string, min, max int64) int64 {
		v, e := integer(d, key, min, max)
		if e != nil {
			invalid = e
		}
		m[key] = v
		return v
	}
	sale := func(id string) M {
		a := find(suiteRecords(s, "sale", t), id)
		if a == nil || str(a, "status") != "Booked" || !can(s, u, t, "view", "sale") {
			invalid = errors.New("Select a booked unit")
		}
		return a
	}
	switch k {
	case "corporate_account":
		number := regexp.MustCompile(`\s+`).ReplaceAllString(str(d, "number"), "")
		if !regexp.MustCompile(`^\d{6,34}$`).MatchString(number) {
			return nil, errors.New("Account needs 6–34 digits")
		}
		for _, a := range s["reras"] {
			if str(a, "number") == number {
				return nil, errors.New("Account already registered as RERA")
			}
		}
		for _, a := range extRows(s, k, "") {
			if str(a, "id") != str(m, "id") && str(a, "number") == number {
				return nil, errors.New("Corporate account already exists")
			}
		}
		if old != nil && str(old, "number") != number {
			return nil, errors.New("Register a new account to change its number")
		}
		m["number"] = number
		text("bank", "Bank")
		text("label", "Purpose")
		ifsc := strings.ToUpper(text("ifsc", "IFSC"))
		m["ifsc"] = ifsc
		if !regexp.MustCompile(`^[A-Z]{4}0[A-Z0-9]{6}$`).MatchString(ifsc) {
			return nil, errors.New("Invalid IFSC")
		}
		m["active"] = d["active"] != false
		if m["active"] == false {
			active := false
			for _, a := range extRows(s, k, "") {
				if str(a, "id") != str(m, "id") && a["active"] == true {
					active = true
				}
			}
			if !active {
				return nil, errors.New("Keep at least one active corporate account")
			}
		}
	case "corporate_payment":
		exp := find(s["expenses"], str(d, "expenseId"))
		account := find(extRows(s, "corporate_account", ""), str(d, "accountId"))
		if exp == nil || str(exp, "tenantId") != t || financeStatus(s, exp) != "Approved" || account == nil || account["active"] != true || !can(s, u, t, "view", "expense") || !can(s, u, t, "view", "corporate_account") {
			return nil, errors.New("Select approved expense and active corporate account")
		}
		m["expenseId"] = str(exp, "id")
		m["accountId"] = str(account, "id")
		for _, key := range []string{"payee", "description", "category"} {
			m[key] = exp[key]
		}
		paid := num("paid", 0, 100000000000000)
		tds := num("tds", 0, 100000000000000)
		if paid+tds <= 0 || paid+tds > corporateBalance(s, exp) {
			return nil, errors.New("Payment + TDS exceeds expense balance")
		}
		date := text("paidDate", "Paid date")
		if !financeDate(date) || date < str(exp, "expenseDate") {
			return nil, errors.New("Paid date must be on or after expense date")
		}
		reference := text("reference", "Bank reference")
		for _, p := range extRows(s, k, "") {
			if str(p, "id") != str(m, "id") && str(p, "accountId") == str(account, "id") && strings.EqualFold(str(p, "reference"), reference) {
				return nil, errors.New("Duplicate payment reference")
			}
		}
		m["amount"] = paid + tds
		m["approvalPolicy"] = 2
		m["creatorSuperuser"] = admin(u)
		if old != nil {
			m["creatorSuperuser"] = old["creatorSuperuser"]
		}
	case "booking":
		a := sale(str(d, "saleId"))
		if invalid != nil {
			return nil, invalid
		}
		m["saleId"] = str(a, "id")
		m["unit"] = str(a, "unit")
		for _, b := range extRows(s, k, t) {
			if str(b, "saleId") == str(a, "id") && str(b, "id") != str(m, "id") {
				return nil, errors.New("Booking already exists for this unit")
			}
		}
		date := text("bookingDate", "Booking date")
		if !financeDate(date) || date != str(a, "date") {
			return nil, errors.New("Booking date must match sale date")
		}
		text("wing", "Wing / block")
		details := obj(d, "details")
		app := obj(details, "applicant")
		if strings.TrimSpace(str(app, "fullName")) == "" || !regexp.MustCompile(`^\+?[0-9 ()-]{7,22}$`).MatchString(str(app, "phone")) || !regexp.MustCompile(`^\S+@\S+\.\S+$`).MatchString(str(app, "email")) {
			return nil, errors.New("Applicant name, phone and valid email required")
		}
		for _, person := range []M{app, obj(details, "coApplicant")} {
			if person["photo"] != nil {
				raw, e := base64.StdEncoding.DecodeString(str(obj(person, "photo"), "base64"))
				if e != nil || len(raw) == 0 || len(raw) > 262144 || !(bytes.HasPrefix(raw, []byte{137, 80, 78, 71, 13, 10, 26, 10}) || bytes.HasPrefix(raw, []byte{255, 216, 255})) {
					return nil, errors.New("Photo must be PNG or JPEG up to 256 KB")
				}
			}
			if str(person, "dob") != "" && !financeDate(str(person, "dob")) {
				return nil, errors.New("Invalid date of birth")
			}
			if str(person, "pan") != "" && !regexp.MustCompile(`^[A-Z]{5}[0-9]{4}[A-Z]$`).MatchString(str(person, "pan")) {
				return nil, errors.New("Invalid PAN")
			}
			if str(person, "aadhaar") != "" && !regexp.MustCompile(`^\d{12}$`).MatchString(str(person, "aadhaar")) {
				return nil, errors.New("Aadhaar needs 12 digits")
			}
		}
		m["details"] = details
		p := obj(d, "pricing")
		tot, e := pricingTotals(p)
		if e != nil {
			return nil, e
		}
		if number(tot, "grandTotal") != number(a, "amount") {
			return nil, errors.New("Sale value must equal Part A + GST + Part B")
		}
		m["pricing"] = p
		m["amount"] = number(tot, "grandTotal")
		if old != nil {
			for _, sch := range extRows(s, "payment_schedule", t) {
				if str(sch, "bookingId") == str(old, "id") && (string(jsonBody(obj(old, "pricing"))) != string(jsonBody(p)) || str(old, "wing") != str(m, "wing") || str(old, "bookingDate") != date) {
					return nil, errors.New("Issued schedules retain original pricing, wing and booking date")
				}
			}
		}
	case "schedule_template":
		text("name", "Schedule name")
		input := arr(d, "rows")
		if len(input) == 0 || len(input) > 50 {
			return nil, errors.New("Add 1–50 instalments")
		}
		seen := map[string]bool{}
		var sum int64
		for _, raw := range input {
			r, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("Invalid instalment")
			}
			code := str(r, "code")
			percent, e := integer(r, "percent", 1, 100)
			if e != nil {
				return nil, e
			}
			_, e = integer(r, "delayDays", 0, 3650)
			if e != nil {
				return nil, e
			}
			if code == "possession" || seen[code] || !regexp.MustCompile(`^\w{1,60}$`).MatchString(code) || strings.TrimSpace(str(r, "label")) == "" || !contains([]any{"booking", "milestone"}, str(r, "trigger")) {
				return nil, errors.New("Unique codes and valid triggers required")
			}
			seen[code] = true
			sum += percent
		}
		if sum != 100 {
			return nil, errors.New("Percentages must total 100")
		}
		m["rows"] = input
		m["version"] = len(extRows(s, k, t)) + 1
	case "payment_schedule":
		b := find(extRows(s, "booking", t), str(d, "bookingId"))
		template := find(extRows(s, "schedule_template", t), str(d, "templateId"))
		if b == nil || template == nil || !can(s, u, t, "view", "booking") {
			return nil, errors.New("Select booking and template")
		}
		a := sale(str(b, "saleId"))
		if invalid != nil {
			return nil, invalid
		}
		for _, sch := range extRows(s, k, t) {
			if str(sch, "saleId") == str(a, "id") {
				return nil, errors.New("Unit already has a schedule")
			}
		}
		m["bookingId"] = str(b, "id")
		m["saleId"] = str(a, "id")
		for _, key := range []string{"unit", "wing", "bookingDate"} {
			m[key] = b[key]
		}
		m["templateId"] = str(template, "id")
		m["templateVersion"] = number(template, "version")
		snapshot, e := buildPaymentSchedule(b, template)
		if e != nil {
			return nil, e
		}
		for key, v := range snapshot {
			m[key] = v
		}
	case "milestone":
		code := text("code", "Milestone")
		found := code == "possession"
		for _, template := range extRows(s, "schedule_template", t) {
			for _, raw := range arr(template, "rows") {
				r := raw.(map[string]any)
				if str(r, "code") == code && str(r, "trigger") == "milestone" {
					found = true
				}
			}
		}
		if !found {
			return nil, errors.New("Choose configured milestone")
		}
		m["wing"] = strings.TrimSpace(str(d, "wing"))
		date := text("date", "Completion date")
		if !financeDate(date) || date > time.Now().UTC().Format("2006-01-02") {
			return nil, errors.New("Completion date must be today or earlier")
		}
		text("note", "Completion note")
	case "demand":
		sch := find(extRows(s, "payment_schedule", t), str(d, "scheduleId"))
		row := M{}
		for _, raw := range arr(sch, "rows") {
			r := raw.(map[string]any)
			if str(r, "code") == str(d, "code") {
				row = r
			}
		}
		a := sale(str(sch, "saleId"))
		bank := find(s["reras"], str(d, "accountId"))
		if sch == nil || str(row, "code") == "" || invalid != nil || bank == nil || str(bank, "tenantId") != t || !can(s, u, t, "view", "payment_schedule") || !can(s, u, t, "view", "rera_account") {
			return nil, errors.New("Select schedule, instalment and project RERA account")
		}
		due := scheduleDue(s, sch, row)
		if due == "" {
			return nil, errors.New("Record milestone completion before demand")
		}
		for _, x := range extRows(s, k, t) {
			if str(x, "scheduleId") == str(sch, "id") && str(x, "code") == str(row, "code") && extensionStatus(s, x) != "Rejected" {
				return nil, errors.New("Instalment already has an active demand")
			}
		}
		for key, v := range row {
			m[key] = v
		}
		m["dueDate"] = due
		m["scheduleId"] = str(sch, "id")
		m["saleId"] = str(a, "id")
		m["unit"] = str(a, "unit")
		m["customer"] = str(a, "customer")
		m["accountId"] = str(bank, "id")
		for _, key := range []string{"number", "bank", "ifsc", "label"} {
			if access(s, u, t, key, "rera_account") == "hidden" {
				return nil, errors.New("RERA bank-detail viewing permissions required")
			}
		}
		m["bank"] = M{"number": str(bank, "number"), "bank": str(bank, "bank"), "ifsc": str(bank, "ifsc"), "label": str(bank, "label")}
		no := text("number", "Demand number")
		for _, x := range extRows(s, k, t) {
			if strings.EqualFold(str(x, "number"), no) {
				return nil, errors.New("Demand number already exists")
			}
		}
		if number(row, "gross") <= 0 {
			return nil, errors.New("No payment due for instalment")
		}
		m["amount"] = number(row, "gross")
		m["approvalPolicy"] = 2
		m["creatorSuperuser"] = admin(u)
	case "receipt_allocation":
		receipt := find(suiteRecords(s, "collection", t), str(d, "receiptId"))
		demand := find(extRows(s, "demand", t), str(d, "demandId"))
		if receipt == nil || demand == nil || extensionStatus(s, demand) != "Approved" || str(receipt, "saleId") != str(demand, "saleId") || !can(s, u, t, "view", "collection") || !can(s, u, t, "view", "demand") {
			return nil, errors.New("Select receipt and approved demand for same unit")
		}
		m["receiptId"] = str(receipt, "id")
		m["demandId"] = str(demand, "id")
		amount := num("amount", 1, 100000000000000)
		if _, ok := d["tds"]; !ok {
			d["tds"] = 0
		}
		tds := num("tds", 0, 100000000000000)
		var allocated, allocatedTds, demandTds int64
		for _, a := range extRows(s, k, t) {
			if str(a, "receiptId") == str(receipt, "id") {
				allocated += number(a, "amount")
				allocatedTds += number(a, "tds")
			}
		}
		for _, a := range extRows(s, k, t) {
			if str(a, "demandId") == str(demand, "id") {
				demandTds += number(a, "tds")
			}
		}
		if tds > amount || allocatedTds+tds > number(receipt, "tds") || demandTds+tds > number(demand, "tds") || allocated-allocatedTds+amount-tds > number(receipt, "amount") || allocated+amount > number(receipt, "amount")+number(receipt, "tds") || amount > demandBalance(s, demand) {
			return nil, errors.New("Allocation exceeds receipt or demand balance")
		}
	}
	if invalid != nil {
		return nil, invalid
	}
	return m, nil
}
func extensionEvent(s State, u M, t string, d M) (M, error) {
	m := find(s["extensions"], str(d, "id"))
	if m == nil || str(m, "tenantId") != t || !contains([]any{"demand", "corporate_payment"}, str(m, "kind")) {
		return nil, errors.New("Approval record not found")
	}
	fake := State{}
	for k, v := range s {
		fake[k] = v
	}
	copy := M{}
	for k, v := range m {
		copy[k] = v
	}
	fake["expenses"] = []M{copy}
	fake["payments"] = nil
	fake["financeEvents"] = s["extensionEvents"]
	if str(m, "kind") == "corporate_payment" && contains([]any{"submit", "approve"}, str(d, "action")) {
		e := find(s["expenses"], str(m, "expenseId"))
		a := find(extRows(s, "corporate_account", ""), str(m, "accountId"))
		if a == nil || a["active"] != true || number(m, "amount") > corporateBalance(s, e) {
			return nil, errors.New("Inactive account or expense balance exceeded")
		}
	}
	return financialEvent(fake, u, t, d)
}
func manageExtensions(tx *sql.Tx, s State, u M, t, kind string, d M) (M, error) {
	if kind == "extension-save" {
		if str(d, "recordKind") == "booking" {
			sale := find(suiteRecords(s, "sale", t), str(d, "saleId"))
			if sale != nil {
				tot, e := pricingTotals(obj(d, "pricing"))
				if e != nil {
					return nil, e
				}
				name := str(obj(obj(d, "details"), "applicant"), "fullName")
				if number(sale, "amount") != number(tot, "grandTotal") || str(sale, "customer") != name {
					input := M{}
					for k, v := range sale {
						input[k] = v
					}
					input["amount"] = number(tot, "grandTotal")
					input["customer"] = name
					updated, e := suiteRecord(s, u, t, "sale", input)
					if e != nil {
						return nil, e
					}
					_, e = tx.Exec("UPDATE operational_records SET body=$1 WHERE id=$2", jsonBody(updated), str(sale, "id"))
					if e != nil {
						return nil, e
					}
					for i, a := range s["suiteRecords"] {
						if str(a, "id") == str(sale, "id") {
							s["suiteRecords"][i] = updated
						}
					}
				}
			}
		}
		m, e := extensionRecord(s, u, t, str(d, "recordKind"), d)
		if e != nil {
			return nil, e
		}
		if find(s["extensions"], str(m, "id")) == nil {
			_, e = tx.Exec("INSERT INTO erp_extensions(id,tenant_id,kind,body) VALUES($1,$2,$3,$4)", str(m, "id"), t, str(m, "kind"), jsonBody(m))
		} else {
			_, e = tx.Exec("UPDATE erp_extensions SET body=$1 WHERE id=$2", jsonBody(m), str(m, "id"))
		}
		if e != nil {
			return nil, e
		}
		return M{"ok": true}, logAudit(tx, u, t, str(m, "kind")+" saved", str(m, "id"))
	}
	event, e := extensionEvent(s, u, t, d)
	if e != nil {
		return nil, e
	}
	_, e = tx.Exec("INSERT INTO erp_extension_events(id,document_id,action,stage,actor_id,body) VALUES($1,$2,$3,$4,$5,$6)", str(event, "id"), str(event, "documentId"), str(event, "action"), str(event, "stage"), str(event, "actorId"), jsonBody(event))
	if e != nil {
		return nil, e
	}
	return M{"ok": true}, logAudit(tx, u, t, "Approval "+str(event, "action"), str(event, "documentId"))
}
func redactExtensions(s State, u M) {
	out := []M{}
	visible := map[string]bool{}
	for _, d := range s["extensions"] {
		t, k := str(d, "tenantId"), str(d, "kind")
		if k == "corporate_account" && !can(s, u, t, "view", k) {
			for _, project := range s["tenants"] {
				if can(s, u, str(project, "id"), "view", k) {
					t = str(project, "id")
					break
				}
			}
		}
		if !can(s, u, t, "view", k) {
			continue
		}
		for group, keys := range map[string][]string{"amount": {"amount", "paid", "tds", "pricing", "rows", "totals", "base", "gst", "gross", "net"}, "reference": {"bookingId", "templateId", "scheduleId", "saleId", "expenseId", "accountId", "bank", "receiptId", "demandId"}, "details": {"details", "bank", "number", "ifsc", "label", "payee", "description", "category", "rows", "unit", "customer", "note"}, "dates": {"bookingDate", "paidDate", "date", "dueDate"}} {
			if access(s, u, t, group, k) == "hidden" {
				for _, key := range keys {
					delete(d, key)
				}
			}
		}
		for key := range obj(d, "data") {
			if access(s, u, t, key, k) == "hidden" {
				delete(obj(d, "data"), key)
			}
		}
		if k == "demand" {
			if !can(s, u, t, "view", "rera_account") {
				delete(d, "bank")
			} else {
				for key := range obj(d, "bank") {
					if access(s, u, t, key, "rera_account") == "hidden" {
						delete(obj(d, "bank"), key)
					}
				}
			}
		}
		visible[str(d, "id")] = true
		out = append(out, d)
	}
	s["extensions"] = out
	events := []M{}
	for _, e := range s["extensionEvents"] {
		if visible[str(e, "documentId")] {
			events = append(events, e)
		}
	}
	s["extensionEvents"] = events
}

package main

import "testing"

func TestPaymentScheduleRoundingAndDueDates(t *testing.T) {
	s := fixture(t)
	b := find(s["extensions"], "booking-sample")
	template := find(s["extensions"], "schedule-reference")
	snapshot, e := buildPaymentSchedule(b, template)
	if e != nil {
		t.Fatal(e)
	}
	var total int64
	for _, raw := range arr(snapshot, "rows") {
		r := raw.(map[string]any)
		if number(r, "base") < 0 || number(r, "gst") < 0 || number(r, "tds") < 0 {
			t.Fatal("Negative allocation")
		}
		total += number(r, "gross")
	}
	if total != number(b, "amount") {
		t.Fatal("Schedule fails reconciliation")
	}
	schedule := find(s["extensions"], "schedule-sample")
	row := arr(schedule, "rows")[1].(map[string]any)
	if scheduleDue(s, schedule, row) != "2026-09-15" {
		t.Fatal("Booking + 5 days")
	}
	s["extensions"] = append(s["extensions"], M{"kind": "milestone", "tenantId": "t1", "code": "excavation", "wing": "A", "date": "2026-10-01"})
	row = arr(schedule, "rows")[2].(map[string]any)
	if scheduleDue(s, schedule, row) != "2026-10-16" {
		t.Fatal("Milestone + 15 days")
	}
}
func TestCorporateAndDemandRecords(t *testing.T) {
	s := fixture(t)
	u := s["users"][0]
	_, e := extensionRecord(s, u, "t1", "corporate_account", M{"number": s["reras"][0]["number"], "bank": "Bank", "ifsc": "HDFC0001234", "label": "Payroll"})
	if e == nil {
		t.Fatal("RERA duplicate allowed")
	}
	a, e := extensionRecord(s, u, "t1", "corporate_account", M{"number": "001234567890", "bank": "Bank", "ifsc": "HDFC0001234", "label": "Payroll"})
	if e != nil {
		t.Fatal(e)
	}
	s["extensions"] = append(s["extensions"], a)
	d, e := extensionRecord(s, u, "t1", "demand", M{"scheduleId": "schedule-sample", "code": "booking", "number": "DEM-GO", "accountId": s["reras"][0]["id"]})
	if e != nil {
		t.Fatal(e)
	}
	if str(d, "dueDate") != "2026-09-10" || number(d, "gross") <= 0 {
		t.Fatal("Invalid demand snapshot")
	}
	_, e = extensionRecord(s, u, "t1", "demand", M{"scheduleId": "schedule-sample", "code": "excavation", "number": "DEM-EXC", "accountId": s["reras"][0]["id"]})
	if e == nil {
		t.Fatal("Missing milestone allowed")
	}
	redactExtensions(s, find(s["users"], "u4"))
	if len(s["extensions"]) != 0 {
		t.Fatal("Private booking information exposed")
	}
}
func TestItemMetadata(t *testing.T) {
	s := fixture(t)
	u := s["users"][0]
	s["forms"] = append(s["forms"], M{"id": "work_order", "version": 2, "fields": []any{}, "itemFields": []any{M{"key": "grade", "label": "Grade", "type": "text", "required": true}}})
	if _, e := itemExtras(s, u, "t1", "work_order", M{}, 0); e == nil {
		t.Fatal("Required item field skipped")
	}
	out, e := itemExtras(s, u, "t1", "work_order", M{"itemData": M{"grade": "M25"}}, 0)
	if e != nil || str(out, "grade") != "M25" {
		t.Fatal("Item metadata lost", e)
	}
	if _, e := itemExtras(s, find(s["users"], "u4"), "t1", "work_order", M{"itemData": M{"grade": "M25"}}, 0); e == nil {
		t.Fatal("Unpermitted item field accepted")
	}
}

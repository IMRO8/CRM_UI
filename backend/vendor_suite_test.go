package main

import (
	"encoding/base64"
	"testing"
)

func TestLabourWorkOrderApprovalChain(t *testing.T) {
	s := fixture(t)
	su := find(s["users"], "u1")
	d := M{"quotationId": "quote1", "vendorId": "v1", "base": int64(50000)}
	contract, e := workContract(s, su, "t1", d)
	if e != nil {
		t.Fatal(e)
	}
	for k, v := range (M{"id": "labour-go", "number": "WO-LABOUR-GO", "tenantId": "t1", "createdBy": "u1", "base": int64(50000), "formId": "work_order"}) {
		contract[k] = v
	}
	s["orders"] = append(s["orders"], contract)
	if effective(s, contract) != 0 {
		t.Fatal("Pending work committed cost")
	}
	step := func(action string) error {
		ev, e := suiteEvent(s, su, "t1", M{"id": "labour-go", "action": action}, true)
		if e == nil {
			s["orderEvents"] = append(s["orderEvents"], ev)
		}
		return e
	}
	if e = step("submit"); e != nil {
		t.Fatal(e)
	}
	if e = step("approve"); e == nil {
		t.Fatal("Labour approved without purchase")
	}
	p := s["purchaseOrders"][0]
	p["orderId"] = "labour-go"
	for _, a := range []string{"submit", "approve"} {
		ev, e := purchaseEvent(s, su, "t1", M{"id": p["id"], "action": a})
		if e != nil {
			t.Fatal(e)
		}
		s["purchaseEvents"] = append(s["purchaseEvents"], ev)
	}
	if e = step("approve"); e != nil {
		t.Fatal(e)
	}
	if effective(s, contract) != 50000 {
		t.Fatal("Approved work missing cost")
	}
	if _, e = workContract(s, su, "t1", d); e == nil {
		t.Fatal("Quotation reused")
	}
}
func TestSuiteApprovalPairAndHistory(t *testing.T) {
	s := fixture(t)
	su := find(s["users"], "u1")
	creator := find(s["users"], "u2")
	fin := find(s["users"], "u3")
	s["suiteRecords"] = nil
	s["suiteEvents"] = nil
	save := func(k string, d M, u M) M {
		m, e := suiteRecord(s, u, "t1", k, d)
		if e != nil {
			t.Fatal(e)
		}
		s["suiteRecords"] = append(s["suiteRecords"], m)
		return m
	}
	r := save("rfq", M{"title": "Tower D", "description": "Labour package", "contractType": "Labour", "weights": M{"price": 50, "delivery": 20, "quality": 20, "experience": 10}}, creator)
	q := save("quotation", M{"rfqId": r["id"], "vendorId": "v1", "number": "QT-GO", "quotationDate": "2026-10-06", "amount": 100000, "deliveryDays": 20, "creditDays": 30, "quality": 4, "experience": 5, "description": "Labour", "terms": "Measured work"}, creator)
	a := save("award", M{"quotationId": q["id"], "reason": "Scope and value", "creatorSuperuser": true}, creator)
	if a["creatorSuperuser"] != false {
		t.Fatal("Forged direct approval")
	}
	for _, step := range []struct {
		u M
		a string
	}{{creator, "submit"}, {fin, "review"}, {su, "approve"}} {
		ev, e := suiteEvent(s, step.u, "t1", M{"id": a["id"], "action": step.a}, false)
		if e != nil {
			t.Fatal(e)
		}
		s["suiteEvents"] = append(s["suiteEvents"], ev)
	}
	if suiteStatus(s, a) == "Approved" {
		t.Fatal("Missing second approver")
	}
	ev, e := suiteEvent(s, fin, "t1", M{"id": a["id"], "action": "approve"}, false)
	if e != nil {
		t.Fatal(e)
	}
	s["suiteEvents"] = append(s["suiteEvents"], ev)
	if suiteStatus(s, a) != "Approved" {
		t.Fatal("Pair not recognized")
	}
	if _, e = suiteRecord(s, creator, "t1", "quotation", q); e == nil {
		t.Fatal("Selected quotation mutable")
	}
}
func TestBOQFileValidation(t *testing.T) {
	valid := M{"name": "signed.pdf", "signed": true, "base64": base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\nBOQ\n%%EOF"))}
	if _, e := boqBytes(valid); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []M{{"name": "x.pdf", "signed": false, "base64": valid["base64"]}, {"name": "x.pdf", "signed": true, "base64": base64.StdEncoding.EncodeToString([]byte("plain text"))}, {"name": "x.pdf", "signed": true, "base64": "corrupt"}} {
		if _, e := boqBytes(bad); e == nil {
			t.Fatal("Bad BOQ accepted")
		}
	}
}
func TestCRMAggregationAndRedaction(t *testing.T) {
	s := fixture(t)
	su := find(s["users"], "u1")
	d := dashboardSummary(s, su)[0]
	if number(d, "progress") != 39 || number(d, "collected") != 115000000 || number(obj(d, "budget"), "workOrders") != 241500000 {
		t.Fatalf("Bad CRM totals: %v", d)
	}
	engineer := dashboardSummary(s, find(s["users"], "u4"))[0]
	if engineer["budget"] != nil || engineer["cashMonths"] != nil || engineer["sales"] != nil {
		t.Fatal("Financial insight leaked")
	}
	if vendorReports(s, find(s["users"], "u4"))[0]["workOrderValue"] != nil {
		t.Fatal("Vendor costs leaked")
	}
	redact(s, find(s["users"], "u4"))
	for _, d := range s["suiteRecords"] {
		if str(d, "kind") == "sale" {
			t.Fatal("Buyer records leaked")
		}
	}
}
func TestCustomMetadataVersionAndReadOnlyPreservation(t *testing.T) {
	s := fixture(t)
	su := find(s["users"], "u1")
	s["forms"] = append(s["forms"], M{"id": "vendor_feedback", "version": 2, "fields": []any{M{"key": "inspection", "label": "Inspection", "type": "number", "required": true}}})
	if _, _, e := financialExtras(s, su, "t1", M{}, "vendor_feedback", nil); e == nil {
		t.Fatal("Required custom field omitted")
	}
	if _, v, e := financialExtras(s, su, "t1", M{"data": M{"inspection": 12}}, "vendor_feedback", nil); e != nil || v != 2 {
		t.Fatalf("Numeric metadata: %v", e)
	}
	engineer := find(s["users"], "u4")
	old := M{"formVersion": 2, "data": M{"inspection": "12"}}
	data, _, e := financialExtras(s, engineer, "t1", M{"data": M{}}, "vendor_feedback", old)
	if e != nil || str(data, "inspection") != "12" {
		t.Fatalf("Hidden old field lost: %v", e)
	}
	if _, _, e = financialExtras(s, engineer, "t1", M{"data": M{"inspection": "14"}}, "vendor_feedback", old); e == nil {
		t.Fatal("Read only custom field changed")
	}
}

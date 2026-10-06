package main

import "testing"

func TestExpenseCostAndApproval(t *testing.T) {
	s := fixture(t)
	su := find(s["users"], "u1")
	creator := find(s["users"], "u2")
	finance := find(s["users"], "u3")
	before := number(costSummary(s, su)[0], "total")
	d, e := financialRecord(s, creator, "t1", "expense", M{"description": "Consultant", "category": "Consultant charges", "payee": "Architect", "expenseDate": "2026-10-05", "amount": int64(20000), "data": M{}})
	if e != nil {
		t.Fatal(e)
	}
	s["expenses"] = append(s["expenses"], d)
	for _, step := range []struct {
		actor  M
		action string
	}{{creator, "submit"}, {finance, "review"}, {finance, "approve"}} {
		event, e := financialEvent(s, step.actor, "t1", M{"id": d["id"], "action": step.action})
		if e != nil {
			t.Fatal(e)
		}
		s["financeEvents"] = append(s["financeEvents"], event)
	}
	if financeStatus(s, d) == "Approved" || number(costSummary(s, su)[0], "total") != before {
		t.Fatal("pending expense counted")
	}
	event, e := financialEvent(s, su, "t1", M{"id": d["id"], "action": "approve"})
	if e != nil {
		t.Fatal(e)
	}
	s["financeEvents"] = append(s["financeEvents"], event)
	if number(costSummary(s, su)[0], "total") != before+20000 {
		t.Fatal("approved expense missing")
	}
	d["referenceId"] = "w1"
	if number(costSummary(s, su)[0], "total") != before {
		t.Fatal("linked expense duplicated")
	}
	if _, e = financialRecord(s, su, "t1", "expense", M{"description": " ", "category": "Consultant", "payee": "Architect", "expenseDate": "2026-10-05", "amount": 100}); e == nil {
		t.Fatal("whitespace accepted")
	}
}
func TestPaymentBalanceAndValidation(t *testing.T) {
	s := fixture(t)
	su := find(s["users"], "u1")
	p := find(s["payments"], "pay1")
	i := find(s["invoices"], "inv1")
	for _, a := range []string{"submit", "approve"} {
		event, e := financialEvent(s, su, "t1", M{"id": p["id"], "action": a})
		if e != nil {
			t.Fatal(e)
		}
		s["financeEvents"] = append(s["financeEvents"], event)
	}
	if invoiceBalance(s, i) != 5000000 {
		t.Fatal("TDS not settled")
	}
	for _, d := range []M{{"invoiceId": "inv1", "paid": 5000001, "tds": 0, "paidDate": "2026-10-05"}, {"invoiceId": "inv1", "paid": 100, "tds": -1, "paidDate": "2026-10-05"}, {"invoiceId": "inv1", "paid": 100, "tds": 0, "paidDate": "2026-02-30"}, {"invoiceId": "inv1", "paid": 100, "tds": 0, "paidDate": "2026-09-30"}} {
		if _, e := financialRecord(s, su, "t1", "payment", d); e == nil {
			t.Fatal("invalid payment accepted", d)
		}
	}
	if _, e := invoiceRecord(s, su, "t1", M{"orderId": "w1", "invoiceNumber": "ssc-2026-041 ", "invoiceDate": "2026-10-05", "description": "Works", "category": "Civil", "amount": 100}); e == nil {
		t.Fatal("duplicate invoice accepted")
	}
}
func TestCostAccessAndInvoiceRedaction(t *testing.T) {
	s := fixture(t)
	u := find(s["users"], "u3")
	if canCost(s, u, "t1") || len(costSummary(s, u)) != 0 {
		t.Fatal("Finance sees cost area")
	}
	r := find(s["roles"], "finance")
	obj(obj(obj(r, "permissions"), "payment"), "fields")["amount"] = "hidden"
	redactFinance(s, u)
	if find(s["invoices"], "inv1")["balance"] != nil || find(s["payments"], "pay1")["tds"] != nil {
		t.Fatal("hidden amount leaked")
	}
	s = fixture(t)
	u = find(s["users"], "u2")
	for _, m := range s["memberships"] {
		if str(m, "userId") == "u2" && str(m, "tenantId") == "t1" {
			m["roleIds"] = append(arr(m, "roleIds"), "admin")
		}
	}
	if !canCost(s, u, "t1") {
		t.Fatal("Admin cost area missing")
	}
}
func TestOnboardingValuesAndPermissions(t *testing.T) {
	s := fixture(t)
	su := find(s["users"], "u1")
	u := find(s["users"], "u2")
	d := M{"name": "Specialist", "trade": "Custom trade", "vendorType": "Custom type", "contactName": "Contact", "phone": "+91 98765 43210", "email": "contact@example.com", "address": "Site office", "pan": "ABCDE1234F"}
	v, e := onboardingRecord(s, su, "t1", d)
	if e != nil || str(v, "trade") != "Custom trade" {
		t.Fatal(v, e)
	}
	if _, e := onboardingRecord(s, u, "t1", d); e == nil {
		t.Fatal("Manager onboarding permission escalated")
	}
	d["email"] = "bad"
	if _, e := onboardingRecord(s, su, "t1", d); e == nil {
		t.Fatal("bad email accepted")
	}
}

package main

import (
	"encoding/json"
	"os"
	"testing"
)

func fixture(t *testing.T) State {
	b, e := os.ReadFile("testdata/seed.json")
	if e != nil {
		t.Fatal(e)
	}
	var s State
	if e = json.Unmarshal(b, &s); e != nil {
		t.Fatal(e)
	}
	return s
}
func TestRedaction(t *testing.T) {
	s := fixture(t)
	u := find(s["users"], "u4")
	redact(s, u)
	if find(s["orders"], "w1")["base"] != nil {
		t.Fatal("financial amount leaked")
	}
	if find(s["amendments"], "a1")["delta"] != nil {
		t.Fatal("amendment amount leaked")
	}
	for _, r := range s["reras"] {
		if str(r, "tenantId") == "t1" {
			t.Fatal("bank account leaked to engineer")
		}
	}
	if obj(find(s["orders"], "w1"), "data")["retention"] != nil {
		t.Fatal("restricted custom field leaked")
	}
}
func TestMultipleRoles(t *testing.T) {
	s := fixture(t)
	u := find(s["users"], "u4")
	if can(s, u, "t1", "approve", "work_order") {
		t.Fatal("wrong project permission")
	}
	if !can(s, u, "t2", "approve", "work_order") {
		t.Fatal("combined roles missing")
	}
}
func TestCents(t *testing.T) {
	for text, want := range map[string]int64{"-0.01": -1, "200000": 20000000, "+1.2": 120} {
		got, e := cents(text)
		if e != nil || got != want {
			t.Fatalf("%s: %d %v", text, got, e)
		}
	}
	for _, text := range []string{"1.234", "1e9", "₹20", "NaN", "9999999999999999"} {
		if _, e := cents(text); e == nil {
			t.Fatalf("accepted %s", text)
		}
	}
}
func TestCSVValidation(t *testing.T) {
	s := fixture(t)
	u := find(s["users"], "u1")
	rows, issues, e := previewCSV(s, u, "t1", "work_order,delta_including_tax,reason\nWO-SKY-001,200000,Add\nWO-SKY-002,-99999999,Reduce\nWO-NGP-001,1,Wrong tenant")
	if e != nil || len(rows) != 1 || len(issues) != 2 {
		t.Fatalf("rows %d errors %d err %v", len(rows), len(issues), e)
	}
	if _, _, e = previewCSV(s, find(s["users"], "u4"), "t1", "x"); e == nil {
		t.Fatal("engineer imported financial data")
	}
}
func TestApprovedTotals(t *testing.T) {
	s := fixture(t)
	if effective(s, find(s["orders"], "w1")) != 140000000 {
		t.Fatal("approved delta omitted")
	}
	if effective(s, find(s["orders"], "w2")) != 48500000 {
		t.Fatal("pending delta applied")
	}
}
func TestVendorProjectAndFieldControls(t *testing.T) {
	s := fixture(t)
	u := find(s["users"], "u4")
	v := find(s["vendors"], "v1")
	v["gstin"] = "restricted-tax-id"
	redactVendor(s, u, "t1", v)
	if v["gstin"] != nil {
		t.Fatal("tax field leaked to site engineer")
	}
	if _, e := vendorRecord(s, "t2", M{"id": "v1", "name": "Wrong project"}); e == nil {
		t.Fatal("vendor edited across projects")
	}
	if _, e := manageVendorUser(nil, s, u, "t1", "vendor", M{"name": "x"}); e == nil {
		t.Fatal("regular user changed vendor")
	}
	if _, e := manageVendorUser(nil, s, u, "t1", "user-create", M{"name": "x"}); e == nil {
		t.Fatal("regular user created credentials")
	}
}

func TestPurchaseDraftAndInventoryRules(t *testing.T) {
	s := fixture(t)
	u := find(s["users"], "u1")
	d := M{"orderId": "w1", "purpose": "Tower A wiring", "number": "PO-FRACTION", "description": "Fractional cable", "vendorId": "v1", "lines": []any{M{"purpose": "Tower A wiring", "material": "Cable", "unit": "metre", "kind": "purchase", "quantityMilli": float64(1250), "rate": float64(101)}}, "data": M{}}
	p, err := purchaseRecord(s, u, "t1", d)
	if err != nil || number(p, "total") != 126 {
		t.Fatalf("fractional purchase: %v %v", p, err)
	}
	if _, err = purchaseRecord(s, find(s["users"], "u4"), "t1", d); err == nil {
		t.Fatal("engineer created purchase order")
	}
	d["vendorId"] = "v6"
	if _, err = purchaseRecord(s, u, "t1", d); err == nil {
		t.Fatal("cross-project vendor accepted")
	}
	if _, err = integer(M{"quantityMilli": 1.5}, "quantityMilli", 1, 100); err == nil {
		t.Fatal("fractional milli quantity accepted")
	}
	p = find(s["purchaseOrders"], "po1")
	e, err := purchaseEvent(s, u, "t1", M{"id": "po1", "action": "submit"})
	if err != nil {
		t.Fatal(err)
	}
	s["purchaseEvents"] = append(s["purchaseEvents"], e)
	e, err = purchaseEvent(s, u, "t1", M{"id": "po1", "action": "approve"})
	if err != nil || str(e, "stage") != "superuser" {
		t.Fatalf("direct superuser approval: %v", err)
	}
	s["purchaseEvents"] = append(s["purchaseEvents"], e)
	if _, err = purchaseEvent(s, u, "t1", M{"id": "po1", "action": "reject"}); err == nil {
		t.Fatal("approved purchase rejected")
	}
	s["stockMovements"] = []M{{"tenantId": "t1", "lotId": "lot", "quantityMilli": int64(2000)}, {"tenantId": "t1", "lotId": "lot", "quantityMilli": int64(-750)}, {"tenantId": "t2", "lotId": "lot", "quantityMilli": int64(750)}}
	if stockBalance(s, "t1", "lot") != 1250 || stockBalance(s, "t2", "lot") != 750 {
		t.Fatal("stock crossed projects")
	}
}
func TestPurchaseFieldRedactionAndProjectAdmin(t *testing.T) {
	s := fixture(t)
	u := find(s["users"], "u4")
	redactProcurement(s, u)
	p := find(s["purchaseOrders"], "po1")
	if p["total"] != nil || arr(p, "lines")[0].(map[string]any)["rate"] != nil {
		t.Fatal("purchase cost leaked")
	}
	if _, err := manageProject(nil, s, u, "tenant", M{"name": "Unauthorized", "code": "BAD"}); err == nil {
		t.Fatal("regular user managed project")
	}
}
func TestSharedRoleRemoval(t *testing.T) {
	s := fixture(t)
	if _, _, err := roleRemoval(s, find(s["users"], "u4"), "manager"); err == nil {
		t.Fatal("regular user deleted role")
	}
	role, assignments, err := roleRemoval(s, find(s["users"], "u1"), "manager")
	if err != nil || str(role, "id") != "manager" || len(assignments) != 3 {
		t.Fatalf("cross-project assignments: %v %v", assignments, err)
	}
	if _, _, err := roleRemoval(s, find(s["users"], "u1"), "missing"); err == nil {
		t.Fatal("missing role deleted")
	}
	if _, err := mutate(nil, s, find(s["users"], "u4"), "t1", "role-delete", M{"id": "manager"}); err == nil {
		t.Fatal("command bypassed superuser check")
	}
}
func TestWorkItemDetails(t *testing.T) {
	s := fixture(t)
	u := find(s["users"], "u1")
	s["purchaseEvents"] = []M{{"id": "approved", "purchaseOrderId": "po1", "action": "approve", "stage": "superuser", "actorId": "u1"}}
	d := M{"base": int64(126), "purchaseOrderId": "po1", "items": []any{M{"description": "Cable installation", "unit": "metre", "quantityMilli": int64(1250), "rate": int64(101), "startDate": "2026-10-01", "endDate": "2026-10-31"}}}
	details, err := workRecordDetails(s, u, "t1", d)
	if err != nil || str(details, "purchaseOrderNumber") != "PO-SKY-001" || number(arr(details, "items")[0].(map[string]any), "amount") != 126 {
		t.Fatalf("details %v err %v", details, err)
	}
	if _, err = workRecordDetails(s, u, "t2", d); err == nil {
		t.Fatal("cross-project PO reference")
	}
	d["base"] = int64(127)
	if _, err = workRecordDetails(s, u, "t1", d); err == nil {
		t.Fatal("mismatched item total")
	}
	details["tenantId"] = "t1"
	redactWorkDetails(s, find(s["users"], "u4"), details)
	line := arr(details, "items")[0].(map[string]any)
	if line["rate"] != nil || line["amount"] != nil || line["quantityMilli"] == nil {
		t.Fatal("item field redaction failed")
	}
}

func TestNewApprovalPairs(t *testing.T) {
	for _, partner := range []string{"admin", "finance"} {
		for _, suFirst := range []bool{false, true} {
			s := fixture(t)
			p := find(s["purchaseOrders"], "po1")
			p["createdBy"] = "u4"
			p["creatorSuperuser"] = false
			m := find(s["users"], "u2")
			for _, membership := range s["memberships"] {
				if str(membership, "userId") == "u2" && str(membership, "tenantId") == "t1" {
					membership["roleIds"] = []any{partner}
				}
			}
			events := []M{{"action": "submit", "actorId": "u4"}, {"action": "review", "actorId": "u2"}}
			actors := []M{m, find(s["users"], "u1")}
			if suFirst {
				actors[0], actors[1] = actors[1], actors[0]
			}
			for i, actor := range actors {
				stage, err := financialApprovalStage(s, actor, "t1", p, events, "purchase_order")
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, M{"action": "approve", "stage": stage, "actorId": str(actor, "id")})
				if (financialStatus(p, events) == "Approved") != (i == 1) {
					t.Fatal("pair finalized at wrong step")
				}
			}
		}
	}
	s := fixture(t)
	p := find(s["purchaseOrders"], "po1")
	p["createdBy"] = "u4"
	p["creatorSuperuser"] = false
	events := []M{{"action": "submit", "actorId": "u4"}, {"action": "review", "actorId": "u2"}}
	if _, err := financialApprovalStage(s, find(s["users"], "u2"), "t1", p, events, "purchase_order"); err == nil {
		t.Fatal("manager treated as admin")
	}
	stage, err := financialApprovalStage(s, find(s["users"], "u3"), "t1", p, events, "purchase_order")
	if err != nil || stage != "finance" {
		t.Fatal(err)
	}
	events = append(events, M{"action": "approve", "stage": "finance", "actorId": "u3"})
	if financialStatus(p, events) == "Approved" {
		t.Fatal("finance alone approved")
	}
	if _, err := financialApprovalStage(s, find(s["users"], "u3"), "t1", p, events, "purchase_order"); err == nil {
		t.Fatal("repeat approver accepted")
	}
	if _, err := financialApprovalStage(s, find(s["users"], "u4"), "t1", p, events, "purchase_order"); err == nil {
		t.Fatal("creator approved")
	}
	if _, err := financialApprovalStage(s, find(s["users"], "u3"), "t3", p, events, "purchase_order"); err == nil {
		t.Fatal("cross-project approval accepted")
	}
}

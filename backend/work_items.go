package main

import (
	"errors"
	"strings"
	"time"
)

var workOrderFields = []string{"amount", "description", "reason", "unit", "period", "quantity", "purchase_reference"}

func workRecordDetails(s State, u M, t string, d M) (M, error) {
	details := M{}
	if raw, exists := d["items"]; exists {
		input, valid := raw.([]any)
		if !valid || len(input) == 0 || len(input) > 100 {
			return nil, errors.New("Add 1–100 work-order items")
		}
		if !fieldsEditable(s, u, t, "work_order", []string{"quantity", "unit", "period"}) {
			return nil, errors.New("Item field editing permissions are required")
		}
		items := []any{}
		var total int64
		for _, entry := range input {
			i, valid := entry.(map[string]any)
			if !valid {
				return nil, errors.New("Invalid work-order item")
			}
			description, unit := strings.TrimSpace(str(i, "description")), strings.TrimSpace(str(i, "unit"))
			start, end := str(i, "startDate"), str(i, "endDate")
			if description == "" || unit == "" || len(description) > 2000 || len(unit) > 50 {
				return nil, errors.New("Each item needs a description and unit")
			}
			if _, err := time.Parse("2006-01-02", start); err != nil {
				return nil, errors.New("Enter a valid period start date")
			}
			if _, err := time.Parse("2006-01-02", end); err != nil || end < start {
				return nil, errors.New("Period end must be on or after the start")
			}
			q, err := integer(i, "quantityMilli", 1, 1000000000)
			if err != nil {
				return nil, err
			}
			rate, err := integer(i, "rate", 0, 100000000000)
			if err != nil {
				return nil, err
			}
			amount := (q/1000)*rate + ((q%1000)*rate+500)/1000
			total += amount
			if total > 100000000000000 {
				return nil, errors.New("Work-order total exceeds the supported range")
			}
			items = append(items, M{"description": description, "unit": unit, "startDate": start, "endDate": end, "quantityMilli": q, "rate": rate, "amount": amount})
		}
		if number(d, "base") != total {
			return nil, errors.New("Work-order total must equal its item amounts")
		}
		details["items"] = items
	}
	if poID := str(d, "purchaseOrderId"); poID != "" {
		if access(s, u, t, "purchase_reference", "work_order") != "edit" || !can(s, u, t, "view", "purchase_order") || access(s, u, t, "order_date", "purchase_order") == "hidden" {
			return nil, errors.New("Purchase-order reference editing and viewing permissions are required")
		}
		p := find(s["purchaseOrders"], poID)
		if p == nil || str(p, "tenantId") != t || poStatus(s, p) != "Approved" {
			return nil, errors.New("Select an approved purchase order in this project")
		}
		date := str(p, "orderDate")
		if date == "" && len(str(p, "createdAt")) >= 10 {
			date = str(p, "createdAt")[:10]
		}
		details["purchaseOrderId"] = poID
		details["purchaseOrderNumber"] = str(p, "number")
		details["purchaseOrderDate"] = date
	}
	return details, nil
}
func redactWorkDetails(s State, u M, o M) {
	t := str(o, "tenantId")
	for _, raw := range arr(o, "items") {
		i, valid := raw.(map[string]any)
		if !valid {
			continue
		}
		if access(s, u, t, "amount", "work_order") == "hidden" {
			delete(i, "rate")
			delete(i, "amount")
		}
		if access(s, u, t, "description", "work_order") == "hidden" {
			delete(i, "description")
		}
		if access(s, u, t, "quantity", "work_order") == "hidden" {
			delete(i, "quantityMilli")
		}
		if access(s, u, t, "unit", "work_order") == "hidden" {
			delete(i, "unit")
		}
		if access(s, u, t, "period", "work_order") == "hidden" {
			delete(i, "startDate")
			delete(i, "endDate")
		}
	}
	if access(s, u, t, "purchase_reference", "work_order") == "hidden" || !can(s, u, t, "view", "purchase_order") {
		delete(o, "purchaseOrderId")
		delete(o, "purchaseOrderNumber")
		delete(o, "purchaseOrderDate")
	}
	if access(s, u, t, "order_date", "purchase_order") == "hidden" {
		delete(o, "purchaseOrderDate")
	}
}

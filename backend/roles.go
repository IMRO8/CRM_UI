package main

import "errors"

func roleRemoval(s State, u M, id string) (M, []M, error) {
	if !admin(u) {
		return nil, nil, errors.New("Only the platform superuser can delete shared roles")
	}
	role := find(s["roles"], id)
	if role == nil {
		return nil, nil, errors.New("Role not found")
	}
	assignments := []M{}
	for _, m := range s["memberships"] {
		if contains(arr(m, "roleIds"), id) {
			assignments = append(assignments, m)
		}
	}
	return role, assignments, nil
}

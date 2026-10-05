-- Role deletion is restricted to platform superusers. The API removes assignments
-- and records the deleted definition in append-only audit history atomically.
CREATE POLICY role_delete ON roles FOR DELETE USING(is_platform_admin());

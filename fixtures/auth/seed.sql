-- Права из БД для state-manager-auth (БД e2e_auth, применяется scripts/e2e.sh после миграций).

-- Тестовый клиент (sub=e2e-client): всё над виджетами в namespace default, в любом шарде.
INSERT INTO auth_roles (name) VALUES ('e2e-widgets-admin');
INSERT INTO auth_role_rules (role_id, verbs, resource_group, namespace, kind)
SELECT id, '{*}', 'e2e.reconcile-kit.dev', 'default', 'e2e-widget' FROM auth_roles WHERE name = 'e2e-widgets-admin';
INSERT INTO auth_role_bindings (role_id, subject_kind, subject_value)
SELECT id, 'subject', 'e2e-client' FROM auth_roles WHERE name = 'e2e-widgets-admin';

-- Группа e2e-readers: только чтение виджетов.
INSERT INTO auth_roles (name) VALUES ('e2e-widgets-reader');
INSERT INTO auth_role_rules (role_id, verbs, resource_group, kind)
SELECT id, '{get,list}', 'e2e.reconcile-kit.dev', 'e2e-widget' FROM auth_roles WHERE name = 'e2e-widgets-reader';
INSERT INTO auth_role_bindings (role_id, subject_kind, subject_value)
SELECT id, 'group', 'e2e-readers' FROM auth_roles WHERE name = 'e2e-widgets-reader';

-- Отключённый binding: e2e-disabled не должен получить права.
INSERT INTO auth_role_bindings (role_id, subject_kind, subject_value, disabled)
SELECT id, 'subject', 'e2e-disabled', true FROM auth_roles WHERE name = 'e2e-widgets-admin';

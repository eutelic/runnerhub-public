PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
PRAGMA busy_timeout=5000;
PRAGMA synchronous=NORMAL;

CREATE TABLE IF NOT EXISTS hosts (
  id TEXT PRIMARY KEY,
  data BLOB NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS pools (
  id TEXT PRIMARY KEY,
  data BLOB NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workers (
  id TEXT PRIMARY KEY,
  data BLOB NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  result TEXT NOT NULL,
  safe_detail TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_created_at ON audit_events(created_at DESC);

CREATE TABLE IF NOT EXISTS agent_commands (
  id TEXT PRIMARY KEY,
  host_id TEXT NOT NULL,
  worker_id TEXT NOT NULL,
  command_type TEXT NOT NULL,
  payload BLOB NOT NULL,
  created_at TEXT NOT NULL,
  lease_until TEXT,
  attempts INTEGER NOT NULL DEFAULT 0,
  acked_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_commands_active_semantic
  ON agent_commands(host_id, worker_id, command_type)
  WHERE acked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agent_commands_delivery
  ON agent_commands(host_id, acked_at, lease_until, created_at);

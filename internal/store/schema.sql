-- bili-saver SQLite schema v1
CREATE TABLE IF NOT EXISTS sources (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  type TEXT NOT NULL CHECK(type IN ('video','space','favlist','season','series')),
  ext_id TEXT NOT NULL,
  extra TEXT NOT NULL DEFAULT '{}',
  title TEXT NOT NULL DEFAULT '',
  watch INTEGER NOT NULL DEFAULT 1,
  sync_interval_minutes INTEGER NOT NULL DEFAULT 0,
  sync_mode TEXT NOT NULL DEFAULT 'interval',
  interval_comments_minutes INTEGER NOT NULL DEFAULT 0,
  interval_danmaku_minutes INTEGER NOT NULL DEFAULT 0,
  interval_subtitles_minutes INTEGER NOT NULL DEFAULT 0,
  layout TEXT NOT NULL DEFAULT '',
  auto_download INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  last_sync_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE(type, ext_id)
);

CREATE TABLE IF NOT EXISTS videos (
  bvid TEXT PRIMARY KEY,
  aid INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  pic TEXT NOT NULL DEFAULT '',
  owner_mid INTEGER NOT NULL DEFAULT 0,
  owner_name TEXT NOT NULL DEFAULT '',
  owner_face TEXT NOT NULL DEFAULT '',
  owner_level INTEGER NOT NULL DEFAULT 0,
  pubdate INTEGER NOT NULL DEFAULT 0,
  duration INTEGER NOT NULL DEFAULT 0,
  pages_count INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'ok' CHECK(status IN ('ok','gone','private')),
  first_archived_at INTEGER NOT NULL,
  last_sync_at INTEGER NOT NULL DEFAULT 0,
  downloaded INTEGER NOT NULL DEFAULT 0,
  local_path TEXT NOT NULL DEFAULT '',
  last_stats_at INTEGER NOT NULL DEFAULT 0,
  last_comments_at INTEGER NOT NULL DEFAULT 0,
  last_danmaku_at INTEGER NOT NULL DEFAULT 0,
  last_subtitles_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_videos_owner ON videos(owner_mid);
CREATE INDEX IF NOT EXISTS idx_videos_status ON videos(status);
CREATE INDEX IF NOT EXISTS idx_videos_pubdate ON videos(pubdate);

CREATE TABLE IF NOT EXISTS pages (
  bvid TEXT NOT NULL,
  cid INTEGER NOT NULL,
  page INTEGER NOT NULL DEFAULT 1,
  part TEXT NOT NULL DEFAULT '',
  duration INTEGER NOT NULL DEFAULT 0,
  first_seen_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  PRIMARY KEY(bvid, cid)
);

CREATE TABLE IF NOT EXISTS video_stats_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  bvid TEXT NOT NULL,
  ts INTEGER NOT NULL,
  view INTEGER NOT NULL DEFAULT 0,
  danmaku INTEGER NOT NULL DEFAULT 0,
  reply INTEGER NOT NULL DEFAULT 0,
  favorite INTEGER NOT NULL DEFAULT 0,
  coin INTEGER NOT NULL DEFAULT 0,
  share INTEGER NOT NULL DEFAULT 0,
  like INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_stats ON video_stats_history(bvid, ts);

CREATE TABLE IF NOT EXISTS comments (
  rpid INTEGER PRIMARY KEY,
  bvid TEXT NOT NULL,
  parent_rpid INTEGER NOT NULL DEFAULT 0,
  root_rpid INTEGER NOT NULL DEFAULT 0,
  member_mid INTEGER NOT NULL DEFAULT 0,
  member_name TEXT NOT NULL DEFAULT '',
  member_face TEXT NOT NULL DEFAULT '',
  member_level INTEGER NOT NULL DEFAULT 0,
  content TEXT NOT NULL DEFAULT '',
  like_count INTEGER NOT NULL DEFAULT 0,
  reply_count INTEGER NOT NULL DEFAULT 0,
  ctime INTEGER NOT NULL DEFAULT 0,
  location TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'visible' CHECK(status IN ('visible','deleted')),
  first_seen_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  deleted_at INTEGER NOT NULL DEFAULT 0,
  raw_json TEXT NOT NULL DEFAULT ''
);
-- 跟踪列表（v7）：源触发器发现新视频并自动下载的记录。
CREATE TABLE IF NOT EXISTS video_tracks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  bvid TEXT NOT NULL UNIQUE,
  source_id INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'queued',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_comments_bvid ON comments(bvid, status);
CREATE INDEX IF NOT EXISTS idx_comments_root ON comments(root_rpid);
CREATE INDEX IF NOT EXISTS idx_comments_seen ON comments(bvid, last_seen_at);

CREATE TABLE IF NOT EXISTS danmaku (
  dmid INTEGER PRIMARY KEY,
  bvid TEXT NOT NULL,
  cid INTEGER NOT NULL,
  timepoint INTEGER NOT NULL DEFAULT 0,
  mode INTEGER NOT NULL DEFAULT 0,
  fontsize INTEGER NOT NULL DEFAULT 25,
  color INTEGER NOT NULL DEFAULT 16777215,
  content TEXT NOT NULL DEFAULT '',
  ctime INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'visible' CHECK(status IN ('visible','deleted')),
  first_seen_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  deleted_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_danmaku_cid ON danmaku(cid, status);
CREATE INDEX IF NOT EXISTS idx_danmaku_seen ON danmaku(cid, last_seen_at);

CREATE TABLE IF NOT EXISTS subtitles (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  bvid TEXT NOT NULL,
  cid INTEGER NOT NULL,
  lang TEXT NOT NULL DEFAULT '',
  lang_doc TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL DEFAULT 'bilibili',
  format TEXT NOT NULL DEFAULT 'bcc',
  file_path TEXT NOT NULL DEFAULT '',
  generator TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  UNIQUE(bvid, cid, lang, origin)
);

CREATE TABLE IF NOT EXISTS tags (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS video_tags (
  bvid TEXT NOT NULL,
  tag_id INTEGER NOT NULL REFERENCES tags(id),
  origin TEXT NOT NULL DEFAULT 'bilibili',
  created_at INTEGER NOT NULL,
  PRIMARY KEY(bvid, tag_id, origin)
);

CREATE TABLE IF NOT EXISTS ai_conclusions (
  bvid TEXT PRIMARY KEY,
  model TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  outline_json TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL DEFAULT 'bilibili:ai-conclusion',
  fetched_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS derived_summaries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  bvid TEXT NOT NULL,
  origin TEXT NOT NULL,
  generator TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  prompt_ver TEXT NOT NULL DEFAULT '',
  lang TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS derived_relations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  src_type TEXT NOT NULL,
  src_id TEXT NOT NULL,
  dst_type TEXT NOT NULL,
  dst_id TEXT NOT NULL,
  relation TEXT NOT NULL,
  weight REAL NOT NULL DEFAULT 1,
  origin TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS embeddings (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  entity_type TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  model TEXT NOT NULL,
  dim INTEGER NOT NULL,
  vec BLOB NOT NULL,
  origin TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE(entity_type, entity_id, model)
);

CREATE TABLE IF NOT EXISTS snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  endpoint TEXT NOT NULL,
  params_json TEXT NOT NULL,
  resp_hash TEXT NOT NULL,
  body_path TEXT NOT NULL,
  status_code INTEGER NOT NULL DEFAULT 0,
  fetched_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_snapshots_ep ON snapshots(endpoint, params_json);

CREATE TABLE IF NOT EXISTS jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  type TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  state TEXT NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','done','failed')),
  progress REAL NOT NULL DEFAULT 0,
  detail TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state, id);

CREATE TABLE IF NOT EXISTS plugins (
  id TEXT PRIMARY KEY,
  version TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  manifest_json TEXT NOT NULL DEFAULT '{}',
  config_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS plugin_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  plugin_id TEXT NOT NULL,
  event TEXT NOT NULL,
  ok INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS videos_fts USING fts5(bvid UNINDEXED, title, description);
CREATE VIRTUAL TABLE IF NOT EXISTS comments_fts USING fts5(rpid UNINDEXED, content);

CREATE TABLE IF NOT EXISTS media_libraries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  root_path TEXT NOT NULL,
  layout TEXT NOT NULL DEFAULT 'movie',
  link_mode TEXT NOT NULL DEFAULT 'auto',
  metadata_format TEXT NOT NULL DEFAULT 'nfo',
  match_rules TEXT NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  UNIQUE(name)
);

-- v8 增量：TeriTeri 站内互动数据（teriteri 前端的社区层，独立于 B 站 origin 数据；
-- 站内用户点赞/投币/收藏/关注/发表的内容，展示时与归档的 B 站数据合并）。
CREATE TABLE IF NOT EXISTS site_video_attitude (
  uid INTEGER NOT NULL,
  bvid TEXT NOT NULL,
  love INTEGER NOT NULL DEFAULT 0,
  unlove INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(uid, bvid)
);
CREATE TABLE IF NOT EXISTS site_video_coins (
  uid INTEGER NOT NULL,
  bvid TEXT NOT NULL,
  coins INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(uid, bvid)
);
CREATE TABLE IF NOT EXISTS site_comment_attitude (
  uid INTEGER NOT NULL,
  rpid INTEGER NOT NULL,
  love INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(uid, rpid)
);
CREATE TABLE IF NOT EXISTS site_favorite_folders (
  fid INTEGER PRIMARY KEY AUTOINCREMENT,
  uid INTEGER NOT NULL,
  name TEXT NOT NULL,
  type INTEGER NOT NULL DEFAULT 0,
  ctime INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS site_favorite_items (
  fid INTEGER NOT NULL,
  bvid TEXT NOT NULL,
  uid INTEGER NOT NULL,
  ctime INTEGER NOT NULL,
  PRIMARY KEY(fid, bvid)
);
CREATE TABLE IF NOT EXISTS site_follows (
  uid INTEGER NOT NULL,
  mid INTEGER NOT NULL,
  ctime INTEGER NOT NULL,
  PRIMARY KEY(uid, mid)
);
CREATE TABLE IF NOT EXISTS site_comments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  bvid TEXT NOT NULL,
  uid INTEGER NOT NULL,
  root_id INTEGER NOT NULL DEFAULT 0,
  parent_id INTEGER NOT NULL DEFAULT 0,
  to_user INTEGER NOT NULL DEFAULT 0,
  content TEXT NOT NULL,
  ctime INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS site_danmaku (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  bvid TEXT NOT NULL,
  cid INTEGER NOT NULL DEFAULT 0,
  uid INTEGER NOT NULL,
  timepoint INTEGER NOT NULL DEFAULT 0,
  mode INTEGER NOT NULL DEFAULT 1,
  fontsize INTEGER NOT NULL DEFAULT 25,
  color INTEGER NOT NULL DEFAULT 16777215,
  content TEXT NOT NULL,
  ctime INTEGER NOT NULL
);

-- v9 增量：站内搜索词库 + 站长个人资料（teriteri 账号自助页）
CREATE TABLE IF NOT EXISTS site_search_words (
  word TEXT PRIMARY KEY,
  uid INTEGER NOT NULL,
  ctime INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS site_profile (
  uid INTEGER PRIMARY KEY,
  nickname TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  gender INTEGER NOT NULL DEFAULT 2,
  avatar TEXT NOT NULL DEFAULT ''
);

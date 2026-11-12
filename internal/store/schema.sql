-- bili-saver SQLite schema v1
-- v13: restartable comment scans; only operational state is pruned, never origin rows.
CREATE TABLE IF NOT EXISTS comment_sync_state (
  bvid TEXT PRIMARY KEY,
  last_full_at INTEGER NOT NULL DEFAULT 0,
  latest_main_ctime INTEGER NOT NULL DEFAULT 0,
  root_cursor INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS comment_scan_state (
  bvid TEXT PRIMARY KEY,
  state_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS comment_scan_roots (
  bvid TEXT NOT NULL,
  root INTEGER NOT NULL,
  next_pn INTEGER NOT NULL DEFAULT 1,
  done INTEGER NOT NULL DEFAULT 0,
  state_json TEXT NOT NULL,
  PRIMARY KEY(bvid,root)
);
CREATE INDEX IF NOT EXISTS idx_comment_scan_ready ON comment_scan_roots(bvid,done,next_pn,root);
CREATE TABLE IF NOT EXISTS comment_scan_seen (
  bvid TEXT NOT NULL,
  rpid INTEGER NOT NULL,
  paged INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(bvid,rpid)
);

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
  user_id INTEGER NOT NULL DEFAULT 0,
  space_scope TEXT NOT NULL DEFAULT '',
  personal_scanned INTEGER NOT NULL DEFAULT 0,
  UNIQUE(user_id, type, ext_id)
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
CREATE INDEX IF NOT EXISTS idx_comment_root_sweep ON comments(bvid,rpid) WHERE root_rpid=0 AND reply_count>0;
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
-- 本地化只扫描头像索引，避免读取大型 raw_json 评论行。
CREATE INDEX IF NOT EXISTS idx_comments_faces ON comments(bvid, member_mid, member_face) WHERE member_mid>0 AND member_face<>'';
CREATE INDEX IF NOT EXISTS idx_comments_root ON comments(root_rpid);
-- 消失对账只查 visible 行，覆盖计数与时间范围，历史 deleted 不参与扫描。
CREATE INDEX IF NOT EXISTS idx_comments_reconcile ON comments(bvid, last_seen_at) WHERE status='visible';
DROP INDEX IF EXISTS idx_comments_seen;

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
-- XML 顺序导出无需临时排序；不索引每次同步变化的 last_seen_at。
CREATE INDEX IF NOT EXISTS idx_danmaku_export ON danmaku(cid, timepoint);
CREATE INDEX IF NOT EXISTS idx_danmaku_reconcile ON danmaku(cid, last_seen_at) WHERE status='visible';
DROP INDEX IF EXISTS idx_danmaku_seen;
-- 控制台列表/卡片按视频计数，cid 索引无法支持该查询。
CREATE INDEX IF NOT EXISTS idx_danmaku_bvid_status ON danmaku(bvid, status);

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
  priority INTEGER NOT NULL DEFAULT 0,
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

-- 仅活动任务进入去重索引；终态历史不增加排发查找成本。
CREATE INDEX IF NOT EXISTS idx_jobs_active_payload ON jobs(type,payload_json) WHERE state IN ('queued','running');
CREATE INDEX IF NOT EXISTS idx_jobs_type_id ON jobs(type,id DESC);

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

-- v14: personal membership is retained after removing a monitor source.
CREATE TABLE IF NOT EXISTS user_tiers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  max_daily_adds INTEGER NOT NULL DEFAULT 10,
  min_interval_minutes INTEGER NOT NULL DEFAULT 60,
  max_daily_downloads INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS user_videos (
  user_id INTEGER NOT NULL,
  bvid TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(user_id,bvid)
);
CREATE TABLE IF NOT EXISTS user_download_requests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  bvid TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'waiting',
  job_id INTEGER NOT NULL DEFAULT 0,
  priority INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(user_id,bvid)
);
CREATE INDEX IF NOT EXISTS idx_user_download_pending ON user_download_requests(state,next_attempt_at,id);
CREATE INDEX IF NOT EXISTS idx_user_download_bvid ON user_download_requests(bvid,state);
CREATE TABLE IF NOT EXISTS user_download_usage (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id INTEGER NOT NULL,
  user_id INTEGER NOT NULL,
  day TEXT NOT NULL,
  state TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_user_download_day ON user_download_usage(user_id,day,state);
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_download_reservation ON user_download_usage(request_id) WHERE state='reserved';

CREATE TABLE IF NOT EXISTS source_videos (
  source_id INTEGER NOT NULL,
  bvid TEXT NOT NULL,
  PRIMARY KEY(source_id,bvid)
);
CREATE INDEX IF NOT EXISTS idx_source_videos_bvid ON source_videos(bvid,source_id);

-- v15: mutable profile fields are separate from stable users.id.
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  pass_hash TEXT NOT NULL,
  salt TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'user',
  tier_id INTEGER NOT NULL DEFAULT 1,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  avatar_path TEXT NOT NULL DEFAULT ''
);

-- v16: authoritative archive observations.

CREATE TABLE IF NOT EXISTS danmaku_sync_state (
 bvid TEXT NOT NULL,cid INTEGER NOT NULL,last_epoch INTEGER NOT NULL DEFAULT 0,
 anomalous INTEGER NOT NULL DEFAULT 0,missing_hash TEXT NOT NULL DEFAULT '',PRIMARY KEY(bvid,cid)
);
CREATE TABLE IF NOT EXISTS collection_status (
 bvid TEXT NOT NULL,cid INTEGER NOT NULL DEFAULT 0,component TEXT NOT NULL,
 attempted_at INTEGER NOT NULL,successful_at INTEGER NOT NULL DEFAULT 0,
 complete_at INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(bvid,cid,component)
);
-- v17: immutable media identities and local projections.

CREATE TABLE IF NOT EXISTS archive_assets (
 id INTEGER PRIMARY KEY AUTOINCREMENT,bvid TEXT NOT NULL,cid INTEGER NOT NULL DEFAULT 0,
 role TEXT NOT NULL,origin TEXT NOT NULL,variant TEXT NOT NULL DEFAULT '',policy_hash TEXT NOT NULL DEFAULT '',
 sha256 TEXT NOT NULL,size INTEGER NOT NULL,storage_key TEXT NOT NULL,provenance TEXT NOT NULL,
 created_at INTEGER NOT NULL,verified_at INTEGER NOT NULL,
 UNIQUE(bvid,cid,role,origin,variant,policy_hash,sha256)
);
CREATE INDEX IF NOT EXISTS idx_assets_identity ON archive_assets(bvid,cid,role,policy_hash,id DESC);
CREATE TABLE IF NOT EXISTS current_pages(bvid TEXT NOT NULL,page INTEGER NOT NULL,cid INTEGER NOT NULL,PRIMARY KEY(bvid,page),UNIQUE(bvid,cid));
CREATE TABLE IF NOT EXISTS page_sets(bvid TEXT PRIMARY KEY,epoch INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS legacy_part_bindings(bvid TEXT NOT NULL,page INTEGER NOT NULL,cid INTEGER NOT NULL,PRIMARY KEY(bvid,page));
CREATE TABLE IF NOT EXISTS video_policies(bvid TEXT PRIMARY KEY,hidden INTEGER NOT NULL DEFAULT 0,updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS storage_roots(id TEXT PRIMARY KEY,path TEXT NOT NULL);

-- v18: transactional runtime configuration.
CREATE TABLE IF NOT EXISTS runtime_settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS settings_revision(id INTEGER PRIMARY KEY CHECK(id=1),revision INTEGER NOT NULL);
INSERT OR IGNORE INTO settings_revision VALUES(1,0);

CREATE TABLE IF NOT EXISTS archive_events(id INTEGER PRIMARY KEY AUTOINCREMENT,event TEXT NOT NULL,payload TEXT NOT NULL,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS event_deliveries(id INTEGER PRIMARY KEY AUTOINCREMENT,event_id INTEGER NOT NULL,target TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',updated_at INTEGER NOT NULL,UNIQUE(event_id,target));
CREATE INDEX IF NOT EXISTS idx_delivery_pending ON event_deliveries(state,next_at,id);
CREATE TABLE IF NOT EXISTS derived_delivery_ops(delivery_id INTEGER NOT NULL,op_index INTEGER NOT NULL,checksum TEXT NOT NULL,PRIMARY KEY(delivery_id,op_index));



CREATE TABLE IF NOT EXISTS library_outputs(library_id INTEGER NOT NULL,root TEXT NOT NULL,key TEXT NOT NULL,fingerprint TEXT NOT NULL,previous TEXT NOT NULL DEFAULT '',PRIMARY KEY(library_id,root,key));
CREATE TABLE IF NOT EXISTS library_groups(library_id INTEGER NOT NULL,group_key TEXT NOT NULL,name TEXT NOT NULL,PRIMARY KEY(library_id,group_key));
CREATE TABLE IF NOT EXISTS library_episodes(library_id INTEGER NOT NULL,group_key TEXT NOT NULL,bvid TEXT NOT NULL,episode INTEGER NOT NULL,PRIMARY KEY(library_id,group_key,bvid),UNIQUE(library_id,group_key,episode));
CREATE TABLE IF NOT EXISTS source_tombstones(source_id INTEGER PRIMARY KEY,type TEXT NOT NULL,ext_id TEXT NOT NULL,title TEXT NOT NULL,extra TEXT NOT NULL,deleted_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS historical_source_videos(source_id INTEGER NOT NULL,bvid TEXT NOT NULL,PRIMARY KEY(source_id,bvid));


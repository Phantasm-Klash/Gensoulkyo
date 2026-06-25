create extension if not exists pgcrypto;

create table if not exists cards (
  id uuid primary key default gen_random_uuid(),
  code text unique not null,
  i18n_key text not null,
  rarity text not null,
  card_type text not null,
  target_type text not null,
  cost int not null check (cost >= 0),
  duration_ticks int not null check (duration_ticks >= 0),
  cooldown_ticks int not null check (cooldown_ticks >= 0),
  effect_json jsonb not null,
  tags text[] not null default '{}',
  season_id text,
  enabled boolean not null default true
);

create table if not exists player_wallets (
  user_id uuid primary key,
  points bigint not null default 0 check (points >= 0),
  card_dust bigint not null default 0 check (card_dust >= 0),
  chest_keys bigint not null default 0 check (chest_keys >= 0),
  updated_at timestamptz not null default now()
);

create table if not exists player_card_inventory (
  user_id uuid not null,
  card_id uuid not null references cards(id),
  copies int not null default 0 check (copies >= 0),
  level int not null default 1 check (level >= 1),
  first_obtained_at timestamptz not null default now(),
  primary key (user_id, card_id)
);

create table if not exists player_decks (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null,
  name text not null,
  format text not null,
  card_ids jsonb not null,
  active boolean not null default false,
  updated_at timestamptz not null default now()
);

create index if not exists idx_player_decks_user_id on player_decks(user_id);

create table if not exists match_rooms (
  id uuid primary key default gen_random_uuid(),
  room_code text unique not null,
  mode text not null,
  status text not null,
  host_user_id uuid not null,
  guest_user_id uuid,
  host_deck_snapshot_json jsonb not null,
  guest_deck_snapshot_json jsonb,
  match_id uuid,
  nakama_match_id text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  expires_at timestamptz not null,
  check (status in ('open', 'ready', 'started', 'closed')),
  check (mode = 'duel')
);

create index if not exists idx_match_rooms_code_status on match_rooms(room_code, status);
create index if not exists idx_match_rooms_host on match_rooms(host_user_id, updated_at desc);
create index if not exists idx_match_rooms_guest on match_rooms(guest_user_id, updated_at desc);

create table if not exists chest_pools (
  id uuid primary key default gen_random_uuid(),
  season_id text not null,
  name text not null,
  cost_json jsonb not null,
  weights_json jsonb not null,
  pity_json jsonb not null,
  starts_at timestamptz not null,
  ends_at timestamptz,
  enabled boolean not null default true
);

create index if not exists idx_chest_pools_active on chest_pools(enabled, starts_at, ends_at);

create table if not exists chest_openings (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null,
  pool_id uuid not null references chest_pools(id),
  server_seed text not null,
  result_json jsonb not null,
  cost_json jsonb not null,
  created_at timestamptz not null default now()
);

create index if not exists idx_chest_openings_user_id on chest_openings(user_id, created_at desc);

create table if not exists external_item_links (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null,
  provider text not null,
  provider_item_id text not null,
  local_card_id uuid references cards(id),
  payload_json jsonb not null,
  created_at timestamptz not null default now(),
  unique (provider, provider_item_id)
);

create table if not exists matches (
  id uuid primary key default gen_random_uuid(),
  mode text not null,
  mode_ruleset_version text,
  ruleset_version text not null,
  server_seed text not null,
  status text not null,
  started_at timestamptz,
  ended_at timestamptz
);

create index if not exists idx_matches_status on matches(status, started_at desc);

create table if not exists match_players (
  match_id uuid not null references matches(id) on delete cascade,
  user_id uuid not null,
  side int not null,
  deck_snapshot_json jsonb not null,
  score bigint not null default 0,
  graze_count int not null default 0,
  hit_count int not null default 0,
  result text,
  reward_json jsonb,
  primary key (match_id, user_id)
);

create table if not exists match_reward_settlements (
  match_id uuid not null references matches(id) on delete cascade,
  user_id uuid not null,
  reward_json jsonb not null,
  created_at timestamptz not null default now(),
  primary key (match_id, user_id)
);

create index if not exists idx_match_reward_settlements_user_id on match_reward_settlements(user_id, created_at desc);

create table if not exists player_ratings (
  user_id uuid not null,
  season_id text not null,
  rating_code text not null,
  rank_score int not null default 0,
  certified_at timestamptz,
  next_unlock_eligible boolean not null default false,
  updated_at timestamptz not null default now(),
  primary key (user_id, season_id, rating_code)
);

create table if not exists rating_challenge_results (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null,
  season_id text not null,
  rating_code text not null,
  stage_id text not null,
  passed boolean not null,
  result_json jsonb not null,
  replay_id uuid,
  created_at timestamptz not null default now()
);

create index if not exists idx_rating_results_user_id on rating_challenge_results(user_id, created_at desc);

create table if not exists boss_instances (
  id uuid primary key default gen_random_uuid(),
  boss_code text not null,
  mode text not null,
  season_id text,
  max_hp bigint not null check (max_hp > 0),
  current_hp bigint not null check (current_hp >= 0),
  friendly_fire_mode text not null,
  starts_at timestamptz not null,
  ends_at timestamptz,
  defeated_at timestamptz
);

create table if not exists boss_attempts (
  id uuid primary key default gen_random_uuid(),
  boss_instance_id uuid not null references boss_instances(id),
  match_id uuid not null references matches(id),
  user_id uuid not null,
  damage bigint not null default 0 check (damage >= 0),
  survived boolean not null,
  clear_result text,
  reward_json jsonb,
  created_at timestamptz not null default now()
);

create table if not exists daily_attempt_limits (
  user_id uuid not null,
  mode text not null,
  date_key text not null,
  used_attempts int not null default 0 check (used_attempts >= 0),
  max_attempts int not null check (max_attempts >= 0),
  updated_at timestamptz not null default now(),
  primary key (user_id, mode, date_key)
);

create table if not exists battle_royale_matches (
  match_id uuid primary key references matches(id) on delete cascade,
  public_pool_json jsonb not null,
  zero_round_order_json jsonb not null,
  round_events_json jsonb not null default '[]'::jsonb
);

create table if not exists match_events (
  id bigserial primary key,
  match_id uuid not null references matches(id) on delete cascade,
  tick int not null check (tick >= 0),
  event_type text not null,
  payload_json jsonb not null
);

create index if not exists idx_match_events_match_tick on match_events(match_id, tick);

insert into cards (code, i18n_key, rarity, card_type, target_type, cost, duration_ticks, cooldown_ticks, effect_json, tags, season_id, enabled)
values
  ('pulse_shot', 'card.pulse_shot', 'common', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('focus_barrier', 'card.focus_barrier', 'common', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('graze_spark', 'card.graze_spark', 'common', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('drift_cancel', 'card.drift_cancel', 'common', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('spiral_field', 'card.spiral_field', 'rare', 'field', 'shared', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('mirror_step', 'card.mirror_step', 'common', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('bomb_fragment', 'card.bomb_fragment', 'rare', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('score_lantern', 'card.score_lantern', 'common', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('slow_charm', 'card.slow_charm', 'common', 'opponent', 'opponent', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('tempo_seal', 'card.tempo_seal', 'rare', 'opponent', 'opponent', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('needle_thread', 'card.needle_thread', 'common', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('orbit_wisp', 'card.orbit_wisp', 'common', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('safe_lane', 'card.safe_lane', 'common', 'field', 'shared', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('chain_voltage', 'card.chain_voltage', 'rare', 'field', 'shared', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('burst_window', 'card.burst_window', 'rare', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('anchor_sigil', 'card.anchor_sigil', 'common', 'field', 'shared', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('starlit_feint', 'card.starlit_feint', 'rare', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('reversal_glyph', 'card.reversal_glyph', 'super_rare', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('crossfire_mark', 'card.crossfire_mark', 'rare', 'opponent', 'opponent', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true),
  ('last_guard', 'card.last_guard', 'super_rare', 'self', 'self', 0, 0, 0, '{"kind":"starter"}'::jsonb, array['starter'], 'open-alpha-0', true)
on conflict (code) do nothing;

insert into chest_pools (id, season_id, name, cost_json, weights_json, pity_json, starts_at, enabled)
values (
  '00000000-0000-0000-0000-000000000101',
  'open-alpha-0',
  'Open Alpha Starter Pool',
  '{"points":0,"card_dust":0,"chest_keys":0}'::jsonb,
  '[
    {"code":"pulse_shot","rarity":"common","weight":10},
    {"code":"focus_barrier","rarity":"common","weight":10},
    {"code":"graze_spark","rarity":"common","weight":10},
    {"code":"drift_cancel","rarity":"common","weight":10},
    {"code":"spiral_field","rarity":"rare","weight":5},
    {"code":"bomb_fragment","rarity":"rare","weight":5},
    {"code":"tempo_seal","rarity":"rare","weight":5},
    {"code":"reversal_glyph","rarity":"super_rare","weight":1},
    {"code":"last_guard","rarity":"super_rare","weight":1}
  ]'::jsonb,
  '{"rare_after":10,"super_rare_after":60,"carries_between_event_pools":false}'::jsonb,
  now(),
  true
)
on conflict (id) do nothing;

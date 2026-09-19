\timing off
\echo ===SETTINGS
select name, setting, unit from pg_settings where name in ('shared_buffers','work_mem','max_connections','effective_cache_size','random_page_cost','maintenance_work_mem','autovacuum','statement_timeout','max_parallel_workers_per_gather','jit');
\echo ===ACTIVITY
select pid, state, wait_event_type, wait_event, now()-query_start as age, left(regexp_replace(query, '\s+', ' ', 'g'), 110) q from pg_stat_activity where datname='csx' and pid<>pg_backend_pid() order by query_start;
\echo ===PGSS_EXT
select extname, extversion from pg_extension;
\echo ===PGSS_TOP_TOTAL
select queryid, calls, round(total_exec_time::numeric/1000,1) total_s, round(mean_exec_time::numeric,1) mean_ms, round(max_exec_time::numeric,1) max_ms, rows, shared_blks_hit hit, shared_blks_read rd, temp_blks_written tmpw, round(blk_read_time::numeric/1000,1) rdio_s, left(regexp_replace(query,'\s+',' ','g'),140) q from pg_stat_statements where dbid=(select oid from pg_database where datname='csx') order by total_exec_time desc limit 30;
\echo ===PGSS_TOP_MEAN
select queryid, calls, round(total_exec_time::numeric/1000,1) total_s, round(mean_exec_time::numeric,1) mean_ms, round(max_exec_time::numeric,1) max_ms, shared_blks_read rd, left(regexp_replace(query,'\s+',' ','g'),140) q from pg_stat_statements where dbid=(select oid from pg_database where datname='csx') and calls>=20 order by mean_exec_time desc limit 20;
\echo ===PGSS_TOP_CALLS
select queryid, calls, round(total_exec_time::numeric/1000,1) total_s, round(mean_exec_time::numeric,2) mean_ms, left(regexp_replace(query,'\s+',' ','g'),140) q from pg_stat_statements where dbid=(select oid from pg_database where datname='csx') order by calls desc limit 20;
\echo ===PGSS_RESET
select stats_reset from pg_stat_statements_info;
\echo ===TABLES_SCANS
select relname, seq_scan, seq_tup_read, idx_scan, n_live_tup, n_dead_tup, last_autovacuum, last_autoanalyze from pg_stat_user_tables order by seq_tup_read desc limit 25;
\echo ===UNUSED_IDX
select s.relname, s.indexrelname, s.idx_scan, pg_size_pretty(pg_relation_size(s.indexrelid)) from pg_stat_user_indexes s join pg_index i on i.indexrelid=s.indexrelid where not i.indisunique and s.idx_scan < 50 order by pg_relation_size(s.indexrelid) desc limit 30;
\echo ===DB
select numbackends, xact_commit, blks_read, blks_hit, round(100.0*blks_hit/nullif(blks_hit+blks_read,0),2) hit_pct, temp_files, pg_size_pretty(temp_bytes) temp, deadlocks, round(blk_read_time/1000) rd_s, stats_reset from pg_stat_database where datname='csx';
\echo ===BGW
select checkpoints_timed, checkpoints_req, buffers_checkpoint, buffers_clean, buffers_backend from pg_stat_bgwriter;

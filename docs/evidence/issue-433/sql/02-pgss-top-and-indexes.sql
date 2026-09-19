\echo ===PGSS_TOP_TOTAL
select queryid, calls, round(total_exec_time::numeric/1000,1) total_s, round(mean_exec_time::numeric,1) mean_ms, round(max_exec_time::numeric,1) max_ms, rows, shared_blks_hit hit, shared_blks_read rd, temp_blks_written tmpw, round(shared_blk_read_time::numeric/1000,1) rdio_s, round(temp_blk_write_time::numeric/1000,1) tmpio_s, left(regexp_replace(query,'\s+',' ','g'),100) q from pg_stat_statements where dbid=(select oid from pg_database where datname='csx') order by total_exec_time desc limit 30;
\echo ===PGSS_TEMP
select queryid, calls, temp_blks_written tmpw, round(temp_blk_write_time::numeric/1000,1) tmpio_s, round(mean_exec_time::numeric,1) mean_ms, left(regexp_replace(query,'\s+',' ','g'),100) q from pg_stat_statements where dbid=(select oid from pg_database where datname='csx') order by temp_blks_written desc limit 12;
\echo ===CKPT
select * from pg_stat_checkpointer;
\echo ===IDX
select tablename, indexname, indexdef from pg_indexes where schemaname='public' and tablename in ('samples','receipts','sample_packages','evidence_agg','failure_clusters','wanted','compatibility_snapshots','cases','verification_jobs','authoring_drafts','dependency_edge','packages','shards','evidence_dedup') order by tablename, indexname;
\echo ===FULLQ
select queryid, query from pg_stat_statements where dbid=(select oid from pg_database where datname='csx') and queryid in (-1838375435808251149,-8919612395518868614,5126368844612351286,5754581964168977175,-8060285611411695903,-7089594382960164887,8649384622017691265,-8292064883623834489,5702087356951575197,-181493637755466483,1844938444130241828,1275464037829188384,-1717588399536768741,7392537151859592127,-204009666064603653,6664698108563708128,1256155084258267957,-1034863950460726248,-827627249904167911,2969576699904556945);

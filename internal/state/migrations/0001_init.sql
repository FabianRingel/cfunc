-- 0001_init: tables for functions and cron jobs, plus NOTIFY triggers
-- so peer replicas hear about mutations within the same session-second.

CREATE TABLE IF NOT EXISTS cfunc_functions (
    name              TEXT PRIMARY KEY,
    project           TEXT NOT NULL DEFAULT 'default',
    bin_path          TEXT NOT NULL,
    env               JSONB NOT NULL DEFAULT '[]'::jsonb,
    layers            JSONB NOT NULL DEFAULT '[]'::jsonb,
    max_concurrency   INT  NOT NULL DEFAULT 4,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS cfunc_cron_jobs (
    id           TEXT PRIMARY KEY,
    project      TEXT NOT NULL DEFAULT 'default',
    schedule     TEXT NOT NULL,
    function     TEXT NOT NULL,
    method       TEXT,
    body         TEXT,
    headers      JSONB,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Notification triggers: payload is "<op>:<name>" so subscribers can
-- decide whether to refetch without scanning the full table.
CREATE OR REPLACE FUNCTION cfunc_notify_function() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM pg_notify('cfunc_functions', 'delete:' || OLD.name);
        RETURN OLD;
    ELSE
        PERFORM pg_notify('cfunc_functions', 'put:' || NEW.name);
        RETURN NEW;
    END IF;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS cfunc_functions_notify ON cfunc_functions;
CREATE TRIGGER cfunc_functions_notify
AFTER INSERT OR UPDATE OR DELETE ON cfunc_functions
FOR EACH ROW EXECUTE FUNCTION cfunc_notify_function();

CREATE OR REPLACE FUNCTION cfunc_notify_cron() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM pg_notify('cfunc_crons', 'delete:' || OLD.id);
        RETURN OLD;
    ELSE
        PERFORM pg_notify('cfunc_crons', 'put:' || NEW.id);
        RETURN NEW;
    END IF;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS cfunc_cron_jobs_notify ON cfunc_cron_jobs;
CREATE TRIGGER cfunc_cron_jobs_notify
AFTER INSERT OR UPDATE OR DELETE ON cfunc_cron_jobs
FOR EACH ROW EXECUTE FUNCTION cfunc_notify_cron();

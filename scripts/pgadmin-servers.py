#!/usr/bin/env python3
"""Render docker/pgadmin/servers.json from the DB_DSN in backend/.env.

pgAdmin's server loader parses servers.json literally -- it does no environment
variable substitution (see pgadmin/tools/import_export_servers/__init__.py,
which is a plain open() + json.loads()). Generating the file keeps DB_DSN the
single source of truth instead of restating the connection in a third place.
"""
import json
import pathlib
import string
import sys
import urllib.parse as up

ROOT = pathlib.Path(__file__).resolve().parent.parent
ENV = ROOT / "backend" / ".env"
TEMPLATE = ROOT / "docker" / "pgadmin" / "servers.json.template"
OUT = ROOT / "docker" / "pgadmin" / "servers.json"

# pgAdmin connects from inside the Compose network, where localhost is the
# pgAdmin container itself. A loopback DSN host means the developer is pointing
# the API at the published port, so pgAdmin must use the service name instead.
SERVICE_NAME = "postgres"
LOOPBACK = {"localhost", "127.0.0.1", "::1", ""}


def read_dsn() -> str:
    if not ENV.exists():
        sys.exit(f"{ENV} not found -- copy backend/.env.example to backend/.env first")
    for line in ENV.read_text().splitlines():
        line = line.strip()
        if line.startswith("DB_DSN") and "=" in line:
            return line.split("=", 1)[1].strip().strip('"').strip("'")
    sys.exit(f"DB_DSN not found in {ENV}")


def main() -> None:
    u = up.urlparse(read_dsn())
    host = u.hostname or ""
    values = {
        "DB_HOST": SERVICE_NAME if host in LOOPBACK else host,
        "DB_PORT": u.port or 5432,
        "DB_NAME": u.path.lstrip("/") or "postgres",
        "DB_USER": up.unquote(u.username or ""),
    }
    if not values["DB_USER"]:
        sys.exit("DB_DSN has no username")

    rendered = string.Template(TEMPLATE.read_text()).substitute(values)
    json.loads(rendered)  # fail loudly here rather than silently in pgAdmin
    OUT.write_text(rendered)
    print(f"servers.json <- {values['DB_USER']}@{values['DB_HOST']}:{values['DB_PORT']}/{values['DB_NAME']}")


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Install the packaged provider beside integrations/github with both CLIs."""
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
PROJECT = "terraform-provider-github-enterprise"


def run(cli, *args, cwd, env):
    result = subprocess.run([cli, *args], cwd=cwd, env=env, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError(f"{cli} {' '.join(args)} failed:\n{result.stdout}\n{result.stderr}")
    return result.stdout


def main():
    dist = Path(sys.argv[1]).resolve()
    version = json.loads((dist / "metadata.json").read_text())["version"]
    archive = dist / f"{PROJECT}_{version}_linux_amd64.zip"
    if not archive.is_file():
        raise ValueError("This smoke test requires the Linux amd64 archive")
    for cli, host in (("terraform", "registry.terraform.io"), ("tofu", "registry.opentofu.org")):
        with tempfile.TemporaryDirectory(prefix=f"provider-{cli}-") as temp:
            root = Path(temp)
            mirror = root / "mirror"
            target = mirror / host / "vmvarela" / "github-enterprise"
            target.mkdir(parents=True)
            shutil.copy2(archive, target / archive.name)
            work = root / "consumer"
            shutil.copytree(ROOT / "examples/complete", work)
            config = work / "main.tf"
            text = config.read_text()
            text, count = re.subn(
                r'(source\s*=\s*"vmvarela/github-enterprise"\s+version\s*=\s*)"[^"]*"',
                r'\1"= ' + version + r'"',
                text)
            if count != 1:
                raise ValueError("Expected exactly one enterprise provider version constraint")
            config.write_text(text)
            cli_config = root / "cli.tfrc"
            cli_config.write_text('provider_installation {\n'
                f'  filesystem_mirror {{\n    path = {json.dumps(str(mirror))}\n    include = ["{host}/vmvarela/github-enterprise"]\n  }}\n'
                f'  direct {{ exclude = ["{host}/vmvarela/github-enterprise"] }}\n'
                '}\n')
            env = os.environ.copy()
            # No cached plugin, inherited credentials or development override may hide a broken package.
            for key in list(env):
                if key.startswith(("TF_", "TOFU_", "GITHUB_")):
                    env.pop(key)
            env.update(TF_CLI_CONFIG_FILE=str(cli_config), TF_IN_AUTOMATION="1", CHECKPOINT_DISABLE="1")
            run(cli, "init", "-backend=false", "-input=false", "-no-color", cwd=work, env=env)
            run(cli, "validate", "-no-color", cwd=work, env=env)
            schemas = json.loads(run(cli, "providers", "schema", "-json", cwd=work, env=env))["provider_schemas"]
            ours = schemas[f"{host}/vmvarela/github-enterprise"]
            official = schemas[f"{host}/integrations/github"]
            expected_resources = {
                "github_enterprise_team", "github_enterprise_team_membership",
                "github_enterprise_team_organizations", "github_enterprise_cost_center",
                "github_enterprise_cost_center_resources",
            }
            expected_data = {
                "github_enterprise_team", "github_enterprise_team_membership",
                "github_enterprise_team_organizations", "github_enterprise_teams",
                "github_enterprise_cost_center", "github_enterprise_cost_centers",
            }
            if set(ours["resource_schemas"]) != expected_resources or set(ours["data_source_schemas"]) != expected_data:
                raise ValueError("Installed package does not match the provider contract")
            if "github_enterprise" not in official["data_source_schemas"]:
                raise ValueError("Official provider did not load alongside the enterprise provider")
            print(f"{cli}: installed {host}/vmvarela/github-enterprise {version}; validated complete example and both provider schemas.")


if __name__ == "__main__":
    main()

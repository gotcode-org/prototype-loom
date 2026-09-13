# GotCode Loom

Loom is a high-performance, Git-first web server and dynamic site aggregator built in Go. It acts as a multi-tenant edge server that pulls raw Markdown and HTML templates directly from remote Git repositories, weaves live API data into them on the fly, and serves the rendered output backed by an SQLite caching layer.

Part of the **[GotCode Collective](https://gotcode.org)**.

## Features

- **Universal Git VFS:** Pulls Markdown and HTML directly from remote Git repositories into memory. Zero disk I/O.
- **Multi-Tenant Edge Routing:** Serve dozens of websites and domains from a single binary and a single configuration file.
- **Zero-Config HTTPS:** Natively negotiates Let's Encrypt certificates (TLS-ALPN-01) on the fly for any configured domain.
- **API Weaving:** Inject live JSON data from any public API directly into your templates at the edge.
- **SQLite Caching:** Millisecond response times with per-site caching rules and automatic invalidation on Git pushes.
- **Stateless Themes:** HTML layouts and CSS live directly in your Git repository. Loom is completely unopinionated.
- **Sovereign Security:** Natively supports private SSH repositories with roadmap support for pulling keys directly from the Aegis Secrets Engine.

## Deployment

It is highly recommended to run Loom under a dedicated system user (e.g., `loom`) for security and SSH key isolation.

### 1. Create the Service User
**For Debian/Ubuntu:**
```bash
sudo useradd -r -m -s /bin/bash loom
```
**For Alpine Linux:**
```bash
sudo addgroup -S loom
sudo adduser -S -D -h /home/loom -s /bin/ash -G loom loom
```

### 2. Install the Binary
Compile the binary and move it to your path, and grant it network capabilities so it can bind to ports 80 and 443 without root:
```bash
make build
sudo cp bin/loom /usr/local/bin/loom
sudo chown root:root /usr/local/bin/loom
# Allow non-root users to bind to low ports
sudo setcap 'cap_net_bind_service=+ep' /usr/local/bin/loom
```

### 3. Configuration Setup (/etc/loom vs /home/loom)

By default, the provided service files look for the configuration file at `/home/loom/server.yaml`. 

If you prefer the standard Linux file hierarchy where configuration lives in `/etc`, you can store the master configuration in `/etc/loom` and link it to the home directory:

```bash
# Create the /etc directory
sudo mkdir /etc/loom
sudo cp server.yaml.example /etc/loom/server.yaml

# Set secure permissions (only loom user can read it)
sudo chown -R loom:loom /etc/loom
sudo chmod 640 /etc/loom/server.yaml

# Create a symlink in /home/loom for easy access
sudo ln -s /etc/loom/server.yaml /home/loom/server.yaml
sudo chown -h loom:loom /home/loom/server.yaml
```
*Note: Because we created a symlink, you do not need to modify the `loom.service` or `alpine-loom.init` files! They will seamlessly follow the link from the home directory to `/etc`.*

### 4. Setup Systemd (Debian/Ubuntu)
1. Copy the unit file: `sudo cp loom.service /etc/systemd/system/loom.service`
2. Reload daemon: `sudo systemctl daemon-reload`
3. Enable & Start: `sudo systemctl enable --now loom`

### 5. Setup OpenRC (Alpine Linux)
1. Copy the init script: `sudo cp alpine-loom.init /etc/init.d/loom`
2. Make it executable: `sudo chmod +x /etc/init.d/loom`
3. Create log files: `sudo touch /var/log/loom.log /var/log/loom.err && sudo chown loom:loom /var/log/loom.*`
4. Enable & Start: `sudo rc-update add loom default && sudo rc-service loom start`

## License

This project is licensed under the GNU Affero General Public License v3.0 (AGPL-3.0) - see the [LICENSE](LICENSE) file for details.

## The Templating Engine

Loom uses the standard Go `html/template` engine to execute data, but uniquely evaluates it **inside your Markdown files** before they are parsed into HTML. This allows you to create dynamic, data-driven static sites entirely from markdown.

### 1. `weave_api`

The `weave_api` function performs a live `GET` request to a public JSON API, parses the response, and injects it into your page context.

```markdown
# Live GitHub Commits

{{ range $commit := weave_api "https://api.github.com/repos/gotcode-org/loom/commits" }}
- **{{ .commit.author.name }}**: {{ .commit.message }}
{{ end }}
```

### 2. `weave_git`

The `weave_git` function allows you to securely clone a completely different repository into memory, parse a specific markdown file, and render it inline within your page. This is incredibly useful for generating live Changelogs or embedding Documentation from distributed microservices!

```markdown
# Aegis Changelog

{{ weave_git "git@ssh.gotcode.org:/git/gotcode/aegis.git" "CHANGELOG.md" }}
```

#### Secure Authentication for `weave_git`

To prevent exposing private SSH key paths inside your public Markdown files, `weave_git` relies on a global **Key Mapping** system in your `server.yaml` file. 

When you define an SSH URL in your markdown, Loom checks the `git_auth` map to find the correct private key for that specific host:

```yaml
git_auth:
  "ssh.gotcode.org": "/home/user/.ssh/internal_rsa"
  "github.com": "/home/user/.ssh/id_rsa"
```

### SSH Man-in-the-Middle (MITM) Protection

To protect against DNS spoofing and MITM attacks when cloning repositories, Loom strictly enforces SSH Host Key verification. It will refuse to connect to a Git repository unless the server's public key signature is found in your `known_hosts` file.

If you are running Loom on a fresh server or inside a Docker container, you **must** generate this file before the engine can boot.

Run this command on your host machine to securely scan and save the signatures:
```bash
# Scan GitHub
ssh-keyscan github.com >> ~/.ssh/known_hosts

# Scan GotCode
ssh-keyscan ssh.gotcode.org >> ~/.ssh/known_hosts
```
*Note: If you are running Loom in Docker, ensure your `~/.ssh` directory is mounted into the container via `volumes` in your `docker-compose.yml`!*


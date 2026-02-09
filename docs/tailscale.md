# Tailscale-Only Access (Tailnet Isolation)

This setup relies on Tailnet ACLs for access control. Only devices in your tailnet can reach the hub.

## Quick Start

```
./scripts/run-tailnet.sh
```

Open the printed URL on any device in your tailnet.

## How it works

- Hub listens on `127.0.0.1:8081` and uses `-auth-mode tailnet`.
- `tailscale serve` exposes the hub over HTTPS to your tailnet.
- Browser connects via HTTPS/WSS on Tailnet.
- No tokens are used; access is enforced by Tailnet ACLs.

## HTTPS vs HTTP (Safari secure connection errors)

If Safari says it **can’t establish a secure connection**, it’s usually one of:

- MagicDNS not enabled on your tailnet
- Your device isn’t on the tailnet
- Tailscale HTTPS cert not provisioned yet

Quick workaround (HTTP on tailnet):

```
TAILSCALE_HTTPS=0 ./scripts/run-tailnet.sh
```

Preferred fix:
- Enable **MagicDNS** in the Tailscale admin console.
- Use the MagicDNS URL printed by the script (HTTPS).

## Terminal QR code

The tailnet runner prints a QR code directly in the terminal (no external tools):

```
./scripts/run-tailnet.sh
```

## Homebrew CLI

Once installed via Homebrew, you can run:

```
runeshell run
```

This starts hub + agent and prints a URL + QR code.

## Optional: Tailnet IP enforcement

You can enable IP checks for `100.64.0.0/10`:

```
TAILNET_ONLY=1 ./scripts/run-tailnet.sh
```

Note: When using `tailscale serve`, the hub may see requests as coming from `127.0.0.1` because Tailscale proxies locally. In that case, the IP check can incorrectly block. Leave it disabled unless you bind the hub directly to the Tailscale IP.

## Tailscale ACL Example

```
{
  "acls": [
    {
      "action": "accept",
      "src": ["user:you@example.com"],
      "dst": ["tag:runeshell-hub:443"]
    }
  ],
  "tagOwners": {
    "tag:runeshell-hub": ["user:you@example.com"]
  }
}
```

## Troubleshooting

- Ensure Tailscale is running and you are logged in.
- If the URL is HTTPS and you see a cert error, run:
  `tailscale cert <hostname>` or use the MagicDNS URL printed by the script.
- If the page loads but terminal is blank, check:
  `/tmp/hubd.log` and `/tmp/agentd.log`.

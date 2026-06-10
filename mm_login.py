#!/usr/bin/env python3
"""Mattermost GitLab SSO login helper.

Opens a browser for GitLab authentication, extracts the session token,
and saves it for future API calls.

Usage:
    python mm_login.py --url https://mattermost.example.com         # Login and save token
    python mm_login.py --url https://mattermost.example.com --test  # Test saved token
    python mm_login.py --show                                       # Show current saved token
    python mm_login.py --clear                                      # Clear saved token
"""

import argparse
import json
import os
import sys
import time
import webbrowser
from pathlib import Path

TOKEN_FILE = Path.home() / ".config/matterbox/mm_token.json"


def save_token(token: str, csrf: str = ""):
    TOKEN_FILE.parent.mkdir(parents=True, exist_ok=True)
    data = {"token": token, "csrf": csrf, "saved_at": time.time()}
    TOKEN_FILE.write_text(json.dumps(data, indent=2))
    print(f"Token saved to {TOKEN_FILE}")


def load_token() -> dict | None:
    if TOKEN_FILE.exists():
        return json.loads(TOKEN_FILE.read_text())
    return None


def get_token(mm_url: str):
    """Get token via browser-based GitLab SSO login."""
    login_url = f"{mm_url}/login/gitlab"
    print(f"Opening browser for GitLab login...")
    print(f"If the browser doesn't open, go to: {login_url}")

    webbrowser.open(login_url)

    print("\nPlease complete the GitLab login in your browser.")
    print("After logging in, you'll need to extract the session token.")
    print("\nHow to get the token:")
    print("1. Open browser DevTools (F12)")
    print(f"2. Go to Application/Storage > Cookies > {mm_url}")
    print("3. Copy the value of 'MMAUTHTOKEN'")
    print("4. Paste it below\n")

    token = input("Enter MMAUTHTOKEN: ").strip()
    if not token:
        print("No token provided. Exiting.")
        sys.exit(1)

    save_token(token)
    return token


def test_token(token: str, mm_url: str) -> bool:
    """Verify the token works by fetching the current user."""
    import urllib.request

    req = urllib.request.Request(
        f"{mm_url}/api/v4/users/me",
        headers={"Authorization": f"Bearer {token}"},
    )
    try:
        with urllib.request.urlopen(req) as resp:
            user = json.loads(resp.read())
            print(f"\nAuthenticated as: {user.get('username')} ({user.get('email')})")
            return True
    except urllib.error.HTTPError as e:
        print(f"\nToken validation failed: {e.code} {e.reason}")
        return False


def main():
    parser = argparse.ArgumentParser(description="Mattermost GitLab SSO login")
    parser.add_argument(
        "--url",
        help="Mattermost base URL, e.g. https://mattermost.example.com "
        "(required to log in or test a token)",
    )
    parser.add_argument("--show", action="store_true", help="Show current saved token")
    parser.add_argument("--clear", action="store_true", help="Clear saved token")
    parser.add_argument("--test", action="store_true", help="Test the saved token")
    args = parser.parse_args()

    if args.show:
        token_data = load_token()
        if token_data:
            print(f"Token: {token_data['token'][:20]}...")
            print(f"Saved: {time.strftime('%Y-%m-%d %H:%M', time.localtime(token_data['saved_at']))}")
        else:
            print("No saved token found.")
        return

    if args.clear:
        if TOKEN_FILE.exists():
            TOKEN_FILE.unlink()
            print("Token cleared.")
        else:
            print("No saved token found.")
        return

    if not args.url:
        parser.error("--url is required (e.g. --url https://mattermost.example.com)")

    if args.test:
        token_data = load_token()
        if not token_data:
            print("No saved token found. Run without --test to login first.")
            sys.exit(1)
        success = test_token(token_data["token"], args.url)
        sys.exit(0 if success else 1)

    # Login flow
    token_data = load_token()
    if token_data:
        print(f"Existing token found (saved {time.strftime('%Y-%m-%d %H:%M', time.localtime(token_data['saved_at']))})")
        if test_token(token_data["token"], args.url):
            print("Token is still valid. Use it for API calls.")
            return
        print("Token expired or invalid. Logging in again...")

    token = get_token(args.url)
    test_token(token, args.url)


if __name__ == "__main__":
    main()

## Summary

## Authority and Security Checklist

- [ ] Client-authored authoritative combat, reward, inventory, and settlement fields are rejected.
- [ ] Authenticated routes validate sequence, timestamp, nonce, and idempotency where applicable.
- [ ] Battle tickets remain short-lived, signed, mode-bound, and player-bound.
- [ ] C++ Battle Server boundaries remain reward/database/Steam-free.
- [ ] Runtime and Nakama contract tests were run locally or are covered by CI.

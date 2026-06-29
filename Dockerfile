FROM golang:1.20-bookworm AS test

WORKDIR /workspace/Gensoulkyo

CMD ["go", "test", "./..."]

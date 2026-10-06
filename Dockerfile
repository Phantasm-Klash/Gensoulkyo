FROM golang:1.27-bookworm AS test

WORKDIR /workspace/Gensoulkyo

CMD ["go", "test", "./..."]

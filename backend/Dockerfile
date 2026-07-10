# ---- build ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# Dependency layer: cached until go.mod/go.sum actually change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO off + static so the binary runs on distroless/static.
# -trimpath strips local paths; -s -w drops the symbol table and DWARF.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- runtime ----
# distroless/static: no shell, no package manager, no libc. Nothing to exec if
# someone finds an RCE. This is why the container healthcheck is `/server -healthcheck`.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]

# ---- deps ----
# Separate stage so node_modules is cached on package-lock.json alone, not on
# every source edit.
FROM node:24-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

# ---- build ----
FROM node:24-alpine AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .

# NEXT_PUBLIC_* is inlined into the client bundle at build time, so it has to be
# present here and not only at runtime. This is the URL the BROWSER uses to
# reach the API; the Next server itself uses BACKEND_INTERNAL_URL at runtime.
ARG NEXT_PUBLIC_API_URL=http://localhost:8080
ENV NEXT_PUBLIC_API_URL=$NEXT_PUBLIC_API_URL

ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

# ---- runtime ----
FROM node:24-alpine AS runner
WORKDIR /app

ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
ENV PORT=3000
ENV HOSTNAME=0.0.0.0

# Run as a non-root user. node:alpine ships a `node` user for exactly this.
USER node

# `output: "standalone"` traces the imports and emits a server.js plus only the
# node_modules it actually needs. Copying that instead of the full tree is the
# difference between a ~1.5GB image and this one.
COPY --from=build --chown=node:node /app/.next/standalone ./
COPY --from=build --chown=node:node /app/.next/static ./.next/static
COPY --from=build --chown=node:node /app/public ./public

EXPOSE 3000

# No shell form: this way the process is PID 1 and receives SIGTERM directly,
# so `docker compose down` is a graceful shutdown rather than a 10s kill.
CMD ["node", "server.js"]

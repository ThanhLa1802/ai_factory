# Next.js production image (standalone output).
#
# Build context = web/:
#   docker build -f web/Dockerfile \
#     --build-arg AI_FACTORY_API_URL=http://server:8080 -t ai-factory/web:dev web
#
# AI_FACTORY_API_URL is baked into next.config rewrites() at build time AND read
# at runtime by the SSE route handler, so pass it as a build arg (and set it on
# the pod env too if it differs).
FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:22-alpine AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . ./
ARG AI_FACTORY_API_URL=http://localhost:8080
ENV AI_FACTORY_API_URL=$AI_FACTORY_API_URL
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

FROM node:22-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    PORT=3000 \
    HOSTNAME=0.0.0.0
ARG AI_FACTORY_API_URL=http://localhost:8080
ENV AI_FACTORY_API_URL=$AI_FACTORY_API_URL
RUN addgroup -S nodejs -g 1001 && adduser -S nextjs -u 1001
COPY --from=builder /app/public ./public
COPY --from=builder --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=builder --chown=nextjs:nodejs /app/.next/static ./.next/static
USER nextjs
EXPOSE 3000
CMD ["node", "server.js"]

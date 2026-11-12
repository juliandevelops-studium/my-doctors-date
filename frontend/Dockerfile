# Build context: the frontend directory (docker build -t praxis/frontend:dev frontend)
FROM node:26-alpine@sha256:6f08a523443ddedf52941874c203475b0ad503e859ad70cb26ab8be9a517dbd5 AS deps
WORKDIR /app
COPY package.json package-lock.json ./
# Exactly the locked versions, production dependencies only, no install scripts.
RUN npm ci --omit=dev --ignore-scripts && npm cache clean --force

FROM node:26-alpine@sha256:6f08a523443ddedf52941874c203475b0ad503e859ad70cb26ab8be9a517dbd5
WORKDIR /app
ENV NODE_ENV=production
# The runtime only executes `node src/server.js`; it never invokes npm.
# Dropping the bundled npm tree removes its transitive dependencies
# (undici, brace-expansion, http-cache-semantics, ...) from the image,
# which the latest official base image still ships vulnerable versions of.
RUN rm -rf /usr/local/lib/node_modules/npm /usr/local/bin/npm /usr/local/bin/npx
COPY --from=deps /app/node_modules ./node_modules
COPY package.json ./
COPY src ./src
USER node
EXPOSE 8080
CMD ["node", "src/server.js"]

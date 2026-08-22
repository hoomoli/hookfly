FROM node:26-alpine@sha256:aadf416b2cdce311a8811ba3f0608a61b77dbf997500e2eafe781b51f6a0b019 AS build

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY index.html tsconfig.json vite.config.ts ./
COPY public ./public
COPY src ./src
RUN npm run build

FROM nginxinc/nginx-unprivileged:alpine@sha256:26b0bf6fbf07297983cb341998d79c831508787de26627dd2a112321b9c3a4af

ENV HOOKFLY_ADMIN_UPSTREAM="" \
    HOOKFLY_SITE_NAME="Hookfly" \
    NGINX_ENVSUBST_FILTER="^HOOKFLY_ADMIN_UPSTREAM$"
COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
COPY --chmod=755 nginx/40-site-name.sh /docker-entrypoint.d/40-site-name.sh
COPY --chown=101:101 --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080

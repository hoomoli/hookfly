FROM node:26-alpine AS build

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY index.html tsconfig.json vite.config.ts ./
COPY public ./public
COPY src ./src
RUN npm run build

FROM nginxinc/nginx-unprivileged:alpine

USER root
RUN apk upgrade --no-cache
USER 101

ENV HOOKFLY_ADMIN_UPSTREAM="" \
    HOOKFLY_SITE_NAME="Hookfly" \
    NGINX_ENVSUBST_FILTER="^HOOKFLY_ADMIN_UPSTREAM$"
COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
COPY --chmod=755 nginx/40-site-name.sh /docker-entrypoint.d/40-site-name.sh
COPY --chown=101:101 --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080

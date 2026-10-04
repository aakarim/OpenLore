# Image for the Deploy to Render button (see render.yaml and docs/render.md).
# Render passes service environment variables as build arguments, so set
# OPENLORE_VERSION on the service to pin a release such as 0.7.3.
ARG OPENLORE_VERSION=latest
FROM ghcr.io/aakarim/openlore:${OPENLORE_VERSION}

COPY --chmod=755 start.sh /app/render-start.sh

ENTRYPOINT ["/app/render-start.sh"]
CMD []

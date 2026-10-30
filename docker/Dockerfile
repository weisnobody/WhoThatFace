FROM python:3.11-slim

WORKDIR /opt/whothatface
COPY worker/requirements.lock /opt/whothatface/worker/requirements.lock
RUN python -m pip install --no-cache-dir --requirement worker/requirements.lock
COPY worker/insightface_worker.py /opt/whothatface/worker/insightface_worker.py
COPY scripts/setup_models.py /opt/whothatface/scripts/setup_models.py
ENV WTFACE_MODEL_ROOT=/opt/whothatface/models
RUN python scripts/setup_models.py --model buffalo_l --root /opt/whothatface/models
ENTRYPOINT ["python", "worker/insightface_worker.py"]

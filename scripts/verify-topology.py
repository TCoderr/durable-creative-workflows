"""Exercise the real, disposable local topology; never run against a remote deployment.

Operator: python scripts/verify-topology.py --phase normal|durability|crash|capabilities|security|outages|events|observability|runtime_smoke
Uses ignored local credentials. It never mutates the database directly and never
synthesises workflow history. Results are written to .local/verification/.
"""
from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
import re
import socket
import statistics
import subprocess
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / '.local' / 'verification'
ENV = dict(line.split('=', 1) for line in (ROOT / '.local/stack.env').read_text().splitlines() if '=' in line)
KEYS = json.loads(ENV['VELIN_API_KEYS'])
TOKEN, OTHER = list(KEYS)[:2]


def port(name: str, default: int) -> int:
    return int(ENV.get('VELIN_PORT_' + name, os.environ.get('VELIN_PORT_' + name, default)))


PORTS = {'api': port('API', 8080), 'worker': port('WORKER', 8081), 'capabilities': port('CAPABILITIES', 18000),
         'frontend': port('FRONTEND', 4173), 'nats': port('NATS', 4222), 'prometheus': port('PROMETHEUS', 9090)}
BASE = f'http://127.0.0.1:{PORTS["api"]}'
CAPABILITIES = f'http://127.0.0.1:{PORTS["capabilities"]}'
COMPOSE = ['docker', 'compose', '--env-file', '.local/stack.env']
WSL_DISTRO = os.environ.get('VELIN_WSL_DISTRO', '')
if os.name == 'nt' and WSL_DISTRO:
    wsl_root = os.environ.get('VELIN_WSL_ROOT')
    if not wsl_root:
        wsl_root = subprocess.check_output(['wsl', '-d', WSL_DISTRO, '--', 'wslpath', '-a', str(ROOT)], text=True).strip()
    COMPOSE = ['wsl', '-d', WSL_DISTRO, '-u', 'root', '--cd', wsl_root, '--'] + COMPOSE


def clean(value: str) -> str:
    for secret in [TOKEN, OTHER, ENV['POSTGRES_PASSWORD'], ENV['CAPABILITIES_TOKEN'], ENV['NATS_TOKEN']]:
        value = value.replace(secret, '[REDACTED]')
    return value


def compose(*args: str, timeout: int = 120) -> str:
    result = subprocess.run(COMPOSE + list(args), cwd=ROOT, capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(clean(f'Docker command failed ({args}): {result.stderr[-2500:]} {result.stdout[-1500:]}'))
    return result.stdout


def api(path: str, method: str = 'GET', body=None, *, token: str | None = TOKEN, key: str | None = None, raw: bytes | None = None, trace_id: str | None = None):
    headers = {'Content-Type': 'application/json'}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if key:
        headers['Idempotency-Key'] = key
    if trace_id:
        headers['traceparent'] = '00-' + trace_id + '-' + uuid.uuid4().hex[:16] + '-01'
    data = raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
    request = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    # The API rate-limits bursts per subject and per address and answers 429 with
    # Retry-After before processing anything, so honouring it and retrying is the
    # correct client behaviour; the limit itself is exercised by the security phase.
    for _ in range(30):
        try:
            with urllib.request.urlopen(request, timeout=15) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as error:
            data = error.read()
            try:
                result = json.loads(data)
            except ValueError:
                result = {'unexpected_response': data.decode(errors='replace')[:150]}
            if error.code == 429:
                time.sleep(max(1.0, float(error.headers.get('Retry-After') or 1)))
                continue
            return error.code, result
    raise AssertionError(f'{method} {path}: rate limited for too long')


def require(path: str, method: str = 'GET', body=None, *, statuses=(200,), **kwargs):
    status, result = api(path, method, body, **kwargs)
    if status not in statuses:
        raise AssertionError(f'{method} {path}: expected {statuses}, got {status}: {clean(json.dumps(result))}')
    return result


TERMINAL = {'COMPLETED', 'REJECTED', 'EXPIRED', 'CANCELLED', 'FAILED'}


def wait_state(identifier: str, stages: set[str], timeout: int = 150):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            status, last = api('/api/v1/workflows/' + identifier)
            if status == 200 and last.get('stage') in stages and (last['stage'] not in TERMINAL or last.get('terminal')):
                return last
            if status == 200 and last.get('stage') in TERMINAL and last['stage'] not in stages:
                raise AssertionError(f'Unexpected workflow terminal state: {last}')
        except (urllib.error.URLError, TimeoutError, ConnectionError):
            pass
        time.sleep(0.4)
    raise AssertionError(f'Workflow {identifier} did not reach {stages}; last={last}')


def health():
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        try:
            if api('/health/ready', token=None)[0] == 200:
                return
        except (urllib.error.URLError, TimeoutError, ConnectionError):
            pass
        time.sleep(0.5)
    raise AssertionError('API never became ready')


def brief(title: str):
    return {'title': title, 'objective': 'Create an editorial identity for a cultural journal with clear hierarchy.',
            'audience': 'Readers of contemporary culture', 'constraints': ['Accessible contrast', 'Respect reduced motion'],
            'prohibited': ['neon gradients'], 'brand_memory': []}


def commission(title: str, *, start=True, trace_id: str | None = None):
    key = str(uuid.uuid4())
    payload = {'brief': brief(title)}
    created = require('/api/v1/commissions', 'POST', payload, key=key, statuses=(201,))
    identifier = created['id']
    duplicate = require('/api/v1/commissions', 'POST', payload, key=key)
    assert duplicate['id'] == identifier
    if start:
        require(f'/api/v1/commissions/{identifier}/start', 'POST', statuses=(202,), trace_id=trace_id)
        require(f'/api/v1/commissions/{identifier}/start', 'POST', statuses=(202,))
    return identifier


def decision(state: dict, action='APPROVE'):
    pending = state['pending_approval']
    return {'decision_id': str(uuid.uuid4()), 'approval_id': pending['id'], 'revision_id': pending['revision_id'],
            'action': action, 'reason_code': 'BRIEF_ALIGNED' if action == 'APPROVE' else 'CREATIVE_REFINEMENT',
            'reason': 'Synthetic local engineering verification by the authorized test operator.'}


def approve(identifier: str, state: dict):
    body = decision(state)
    require(f'/api/v1/workflows/{identifier}/decisions', 'POST', body, statuses=(200, 202))
    require(f'/api/v1/workflows/{identifier}/decisions', 'POST', body, statuses=(200, 202))
    completed = wait_state(identifier, {'COMPLETED'})
    lineage = require('/api/v1/provenance/' + identifier)
    approvals = [d for d in lineage['decisions'] if d['action'] == 'APPROVE']
    assert len(approvals) == 1 and approvals[0]['decision_id'] == body['decision_id']
    assert approvals[0]['revision_id'] == body['revision_id']
    artifact = require('/api/v1/artifacts/' + completed['artifact_id'])
    assert artifact['decision']['decision_id'] == body['decision_id']
    assert artifact['revision']['id'] == body['revision_id']
    assert artifact['provenance']['approval_id'] == body['approval_id']
    assert len({step['step_id'] for step in artifact['steps']}) == len(artifact['steps'])
    assert len(artifact['steps']) >= 8
    for step in artifact['steps']:
        assert len(step['provenance']['prompt_hash']) == 64
        assert step['provenance']['live'] is False
        assert step['provenance']['provider'].startswith('deterministic')
    rows = [row for row in lineage['provenance'] if row['capability'] == 'artifact']
    assert len(rows) == 1 and rows[0]['decision_id'] == body['decision_id'] and rows[0]['revision_id'] == body['revision_id']
    return completed, lineage, artifact


def temporal(identifier: str, operation='describe'):
    return json.loads(compose('exec', '-T', 'temporal', 'temporal', 'workflow', operation, '--workflow-id', identifier, '--output', 'json'))


def history(identifier: str):
    raw = temporal(identifier, 'show')
    return raw.get('history', raw)


def publish_hint(identifier: str, count: int = 2):
    payload = json.dumps({'event_id': 'replayed-hint', 'type': 'workflow.completed', 'artifact_id': 'forged'}).encode()
    with socket.create_connection(('127.0.0.1', PORTS['nats']), timeout=5) as connection:
        connection.recv(4096)
        connection.sendall(('CONNECT ' + json.dumps({'auth_token': ENV['NATS_TOKEN']}) + '\r\n').encode())
        for _ in range(count):
            connection.sendall(f'PUB velin.progress.{identifier} {len(payload)}\r\n'.encode() + payload + b'\r\n')
        connection.sendall(b'PING\r\n')
        response = b''
        while b'PONG' not in response:
            response += connection.recv(4096)
            if b'-ERR' in response:
                raise AssertionError('NATS rejected test publication')


def normal():
    started = time.monotonic()
    identifier = commission('Runtime normal commission')
    state = wait_state(identifier, {'WAITING_FOR_APPROVAL'})
    assert state['can_approve'] is True and state['pending_approval']['revision_id'] == state['revision']['id']
    before = require('/api/v1/provenance/' + identifier)
    publish_hint(identifier)
    after = require('/api/v1/provenance/' + identifier)
    assert before == after
    assert require('/api/v1/workflows/' + identifier)['stage'] == 'WAITING_FOR_APPROVAL'
    completed, lineage, artifact = approve(identifier, state)
    record = require(f'/api/v1/workflows/{identifier}/record')
    types = [event['type'] for event in record['events']]
    for expected in ['commission.received', 'run.started', 'step.completed', 'revision.created', 'approval.requested', 'decision.recorded', 'artifact.recorded', 'workflow.completed']:
        assert expected in types, expected
    assert len(record['runs']) == 1 and record['runs'][0]['attempt'] == 1
    save('temporal-history-normal', history(identifier))
    return {'commission_id': identifier, 'completed': completed, 'unique_steps': len(lineage['steps']),
            'single_approval': True, 'duplicate_start_safe': True, 'duplicate_nats_hint_safe': True,
            'record_event_types': types, 'seconds_to_review_including_fixture_latency': time.monotonic() - started}


def durability():
    identifier = commission('Durable approval wait')
    before = wait_state(identifier, {'WAITING_FOR_APPROVAL'})
    describe_before = temporal(identifier)
    compose('stop', 'api', 'worker')
    stopped = compose('ps', '-a', '--format', 'json', 'api', 'worker')
    try:
        during = temporal(identifier)
    finally:
        compose('start', 'api', 'worker')
    health()
    after = wait_state(identifier, {'WAITING_FOR_APPROVAL'})
    assert before['pending_approval'] == after['pending_approval'], 'the pending approval survived the restart unchanged'
    assert before['revision'] == after['revision']
    completed, _, _ = approve(identifier, after)
    save('temporal-history-durability', history(identifier))
    return {'commission_id': identifier, 'before': before, 'after': after, 'temporal_before': describe_before,
            'temporal_while_api_worker_stopped': during, 'stopped_processes': stopped, 'completed': completed}


def set_fault(fault: str):
    override = ROOT / '.local' / 'faults-compose.json'
    override.write_text(json.dumps({'services': {'capabilities': {'environment': {'VELIN_TEST_FAULT': fault}}}}))
    compose('-f', 'compose.yaml', '-f', '.local/faults-compose.json', 'up', '-d', '--no-deps', 'capabilities')
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(CAPABILITIES + '/health/ready', timeout=3) as response:
                if response.status == 200:
                    return
        except (urllib.error.URLError, TimeoutError, ConnectionError):
            pass
        time.sleep(0.4)
    raise AssertionError('capability service failed readiness after fault change')


def crash():
    set_fault('slow_activity')
    identifier = ''
    try:
        identifier = commission('Worker crash recovery')
        deadline = time.monotonic() + 60
        started = None
        while time.monotonic() < deadline:
            description = temporal(identifier)
            activities = description.get('pendingActivities', description.get('pending_activities', []))
            current = require(f'/api/v1/workflows/{identifier}')
            if current['stage'] == 'STRATEGY' and any('RunCapability' in json.dumps(a) and 'STARTED' in json.dumps(a) for a in activities):
                started = description
                break
            time.sleep(0.4)
        assert started, 'No started capability activity was observed before crash injection'
        accepted_before = require(f'/api/v1/provenance/{identifier}')['steps']
        assert {step['capability'] for step in accepted_before} == {'brief', 'research'}
        compose('kill', '-s', 'SIGKILL', 'worker')
        crashed = compose('ps', '-a', '--format', 'json', 'worker')
        set_fault('')
        compose('start', 'worker')
        state = wait_state(identifier, {'WAITING_FOR_APPROVAL'}, timeout=210)
        completed, lineage, _ = approve(identifier, state)
        recorded = history(identifier)
        save('temporal-history-crash', recorded)
        serialized = json.dumps(recorded)
        assert '"attempt": 2' in serialized or '"attempt":2' in serialized, 'Recovery retry evidence is absent from Temporal history'
        record = require(f'/api/v1/workflows/{identifier}/record')
        attempts = [event for event in record['events'] if event['type'] == 'step.attempted' and event['detail'].get('attempt', 1) > 1]
        assert attempts, 'the record does not show the retried attempt'
        assert len(record['runs']) == 1, 'recovery stayed inside the same run'
        accepted_after = {step['step_id']: step for step in lineage['steps']}
        for step in accepted_before:
            assert accepted_after[step['step_id']] == step, 'accepted evidence changed across recovery'
            assert sum(event['type'] == 'step.completed' and event['detail']['step_id'] == step['step_id'] for event in record['events']) == 1
        return {'commission_id': identifier, 'started_activity': started, 'killed_worker': crashed, 'completed': completed,
                'unique_durable_steps': len(lineage['steps']), 'accepted_steps_preserved': len(accepted_before),
                'retry_in_real_history': True, 'retried_attempts_in_record': len(attempts)}
    finally:
        set_fault('')
        compose('start', 'worker')


def capabilities():
    """Real HTTP capability failures retain evidence without accepting invalid output."""
    set_fault('primary_failure')
    try:
        recovered = commission('Capability provider retry evidence')
        waiting = wait_state(recovered, {'WAITING_FOR_APPROVAL'})
        _, lineage, _ = approve(recovered, waiting)
        first = next(step for step in lineage['steps'] if step['capability'] == 'brief')
        assert [item['outcome'] for item in first['invocations']] == ['PROVIDER_UNAVAILABLE', 'PROVIDER_UNAVAILABLE', 'success']
        assert first['provenance']['provider'] == 'deterministic-fallback'
        assert all(not item['live'] for step in lineage['steps'] for item in step['invocations'])
        set_fault('malformed_always')
        failed = commission('Invalid capability output is refused')
        terminal = wait_state(failed, {'FAILED'})
        assert terminal['last_error'] == 'STRUCTURED_OUTPUT_INVALID'
        rejected = require('/api/v1/provenance/' + failed)
        assert not rejected['steps'] and not rejected['artifacts'] and not rejected['decisions']
        record = require(f'/api/v1/workflows/{failed}/record')
        failures = [event for event in record['events'] if event['type'] == 'step.failed']
        assert len(failures) == 1 and failures[0]['detail']['code'] == 'STRUCTURED_OUTPUT_INVALID'
        save('temporal-history-capability-failure', history(failed))
        return {'recovered_commission_id': recovered, 'failed_commission_id': failed,
                'provider_retry_and_fallback_evidence': True, 'invalid_output_never_accepted': True,
                'nonretryable_failure_attempts': len(failures), 'explicit_terminal_state': terminal['stage']}
    finally:
        set_fault('')


def security():
    identifier = commission('Ownership and request boundaries')
    waiting = wait_state(identifier, {'WAITING_FOR_APPROVAL'})
    decisions_path = f'/api/v1/workflows/{identifier}/decisions'
    cross_paths = [f'/api/v1/commissions/{identifier}', f'/api/v1/workflows/{identifier}', f'/api/v1/workflows/{identifier}/record',
                   f'/api/v1/workflows/{identifier}/timeline', f'/api/v1/workflows/{identifier}/events', f'/api/v1/provenance/{identifier}',
                   f'/api/v1/workflows/{identifier}/runs', f'/api/v1/workflows/{identifier}/approvals', f'/api/v1/workflows/{identifier}/decisions']
    for path in cross_paths:
        assert api(path, token=OTHER)[0] == 404, path
    assert api(decisions_path, 'POST', decision(waiting), token=OTHER)[0] == 404
    assert api(f'/api/v1/commissions/{identifier}/publications', 'POST', token=OTHER)[0] == 404
    assert api('/api/v1/commissions')[0] == 200
    assert api('/api/v1/commissions', token='invalid')[0] == 401
    assert api('/api/v1/commissions', 'POST', key=str(uuid.uuid4()), raw=b'{broken')[0] == 400
    assert api('/api/v1/commissions', 'POST', key=str(uuid.uuid4()), raw=b'x' * 40000)[0] == 413
    oversized = {'brief': brief('Oversized')}; oversized['brief']['objective'] = 'x' * 2001
    assert api('/api/v1/commissions', 'POST', oversized, key=str(uuid.uuid4()))[0] == 400
    spoofed = decision(waiting); spoofed['actor_id'] = KEYS[TOKEN]
    assert api(decisions_path, 'POST', spoofed)[0] == 400
    stale = decision(waiting); stale['revision_id'] = str(uuid.uuid4())
    status, body = api(decisions_path, 'POST', stale)
    assert status == 409 and body['code'] == 'STALE_APPROVAL' and body['pending_approval']['id'] == waiting['pending_approval']['id']
    wrong_round = decision(waiting); wrong_round['approval_id'] = str(uuid.uuid4())
    assert api(decisions_path, 'POST', wrong_round)[0] == 409
    revised = decision(waiting, 'REVISE')
    require(decisions_path, 'POST', revised, statuses=(202,))
    deadline = time.monotonic() + 120
    newer = waiting
    while time.monotonic() < deadline:
        newer = wait_state(identifier, {'WAITING_FOR_APPROVAL'})
        if newer['review_round'] > waiting['review_round']:
            break
        time.sleep(0.4)
    assert newer['review_round'] == waiting['review_round'] + 1
    assert newer['pending_approval']['revision_id'] != waiting['pending_approval']['revision_id'], 'the new round names a new revision'
    late = decision(waiting)  # still names the old approval and revision
    status, body = api(decisions_path, 'POST', late)
    assert status == 409 and body['code'] == 'STALE_APPROVAL', 'a decision on the old revision is refused after revision'
    completed, _, artifact = approve(identifier, newer)
    assert artifact['revision']['number'] == 2
    assert api('/api/v1/artifacts/' + completed['artifact_id'], token=OTHER)[0] == 404
    published = require(f'/api/v1/commissions/{identifier}/publications', 'POST', statuses=(201,))
    public = require('/api/v1/public/records/' + published['publication_id'], token=None)
    assert public['public'] is True and 'direction' not in public['workflow']
    assert KEYS[TOKEN] not in json.dumps(public), 'public record leaks the owner subject'
    concurrent_ids = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
        concurrent_ids = list(pool.map(commission, ['Concurrent A', 'Concurrent B', 'Concurrent C']))
    assert len(set(concurrent_ids)) == 3
    for other in concurrent_ids:
        ready = wait_state(other, {'WAITING_FOR_APPROVAL'})
        assert ready['commission_id'] == other
        if other == concurrent_ids[0]:
            require(f'/api/v1/workflows/{other}/decisions', 'POST', decision(ready, 'REJECT'), statuses=(202,))
            wait_state(other, {'REJECTED'})
        else:
            require(f'/api/v1/workflows/{other}/cancel', 'POST', statuses=(202,))
            wait_state(other, {'CANCELLED'})
            assert require(f'/api/v1/workflows/{other}/approvals')['items'][0]['status'] == 'cancelled'
    logs = compose('logs', '--no-color', 'api', 'worker', 'capabilities', timeout=60)
    for value in [TOKEN, OTHER, ENV['CAPABILITIES_TOKEN'], ENV['POSTGRES_PASSWORD'], ENV['NATS_TOKEN']]:
        assert value not in logs, 'Secret detected in application logs'
    save('temporal-history-revision', history(identifier))
    return {'commission_id': identifier, 'cross_owner_routes_blocked': cross_paths + ['decisions', 'publications', 'artifacts'],
            'malformed_oversized_actor_spoof_rejected': True, 'stale_approval_rejected_before_and_after_revision': True,
            'human_revision_executed': True, 'concurrent_workflow_ids': concurrent_ids, 'rejection_and_cancellation_executed': True,
            'public_record_redacted': True, 'known_local_secrets_absent_from_application_logs': True}


def outages():
    identifier = commission('NATS outage resilience', start=False)
    compose('stop', 'nats')
    try:
        assert api('/health/ready', token=None)[0] == 200, 'Optional NATS should not gate readiness'
        require(f'/api/v1/commissions/{identifier}/start', 'POST', statuses=(202,))
        waiting = wait_state(identifier, {'WAITING_FOR_APPROVAL'})
    finally:
        compose('start', 'nats')
    compose('stop', 'postgres')
    try:
        assert api('/health/live', token=None)[0] == 200
        assert api('/health/ready', token=None)[0] == 503
        assert api('/api/v1/commissions')[0] == 503
    finally:
        compose('start', 'postgres')
    health()
    after = wait_state(identifier, {'WAITING_FOR_APPROVAL'})
    assert after['pending_approval'] == waiting['pending_approval']
    completed, _, _ = approve(identifier, after)
    cancelled = commission('Terminal evidence across database outage')
    wait_state(cancelled, {'WAITING_FOR_APPROVAL'})
    compose('stop', 'postgres')
    try:
        compose('exec', '-T', 'temporal', 'temporal', 'workflow', 'cancel', '--workflow-id', cancelled)
        deadline = time.monotonic() + 25
        pending = None
        while time.monotonic() < deadline:
            pending = temporal(cancelled)
            activities = pending.get('pendingActivities', pending.get('pending_activities', []))
            if any('RecordEvent' in json.dumps(item) and int(item.get('attempt', 0)) >= 4 for item in activities):
                break
            time.sleep(0.5)
        else:
            raise AssertionError('Terminal persistence did not retry beyond the ordinary three-attempt limit')
        assert pending['workflowExecutionInfo']['status'] == 'WORKFLOW_EXECUTION_STATUS_RUNNING'
    finally:
        compose('start', 'postgres')
    health()
    final = wait_state(cancelled, {'CANCELLED'})
    record = require(f'/api/v1/workflows/{cancelled}/record')
    assert sum(event['type'] == 'workflow.cancelled' for event in record['events']) == 1
    save('temporal-history-terminal-recovery', history(cancelled))
    return {'commission_id': identifier, 'workflow_advanced_without_nats': True,
            'postgres_outage_live_200_ready_503': True, 'completed_after_dependencies_recovered': completed,
            'terminal_persistence_retried_beyond_three_attempts': True,
            'cancelled_after_database_recovery': final, 'terminal_pending_during_outage': pending}


def events():
    """Duplicate and forged hints never change state; the SSE stream dedupes; the timeline cursor is monotonic."""
    identifier = commission('Duplicate event tolerance')
    waiting = wait_state(identifier, {'WAITING_FOR_APPROVAL'})
    publish_hint(identifier, count=5)
    request = urllib.request.Request(BASE + f'/api/v1/workflows/{identifier}/events', headers={'Authorization': 'Bearer ' + TOKEN, 'Accept': 'text/event-stream'})
    with urllib.request.urlopen(request, timeout=15) as response:
        publish_hint(identifier, count=5)
        collected = b''
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            chunk = response.read1(4096) if hasattr(response, 'read1') else response.read(4096)
            if not chunk:
                break
            collected += chunk
            if collected.count(b': heartbeat') >= 3:
                break
    text = collected.decode(errors='replace')
    states = re.findall(r'event: state\ndata: (.*)\n', text)
    assert states, 'no state snapshot was streamed'
    assert len({s for s in states}) == len(states), 'duplicate hints produced duplicate state snapshots'
    assert all(json.loads(s)['stage'] == 'WAITING_FOR_APPROVAL' for s in states), 'a forged hint changed the streamed stage'
    first = require(f'/api/v1/workflows/{identifier}/timeline')['items']
    cursor = first[-1]['sequence']
    again = require(f'/api/v1/workflows/{identifier}/timeline?after={cursor}')['items']
    assert not again, 'events were delivered twice across the cursor'
    assert [item['sequence'] for item in first] == sorted({item['sequence'] for item in first}), 'sequence is not strictly increasing and unique'
    completed, _, _ = approve(identifier, waiting)
    return {'commission_id': identifier, 'streamed_state_snapshots': len(states), 'forged_hints_ignored': True, 'timeline_cursor_monotonic': True, 'completed': completed}


def runtime_smoke():
    services = ['api', 'worker', 'capabilities', 'frontend']

    def states():
        raw = compose('ps', '-a', '--format', 'json')
        rows = json.loads(raw) if raw.lstrip().startswith('[') else [json.loads(line) for line in raw.splitlines() if line.strip()]
        return {row['Service']: {key: row.get(key) for key in ['State', 'Health', 'ExitCode', 'Image', 'Name']} for row in rows}

    before = states()
    for service in ['postgres', 'temporal', 'nats', *services, 'otel', 'prometheus']:
        assert before[service]['State'] == 'running', (service, before[service])
    ports = {name: PORTS[name] for name in ['api', 'worker', 'capabilities', 'frontend']}
    for service, port in ports.items():
        for route in ['live', 'ready']:
            with urllib.request.urlopen(f'http://127.0.0.1:{port}/health/{route}', timeout=5) as response:
                assert response.status == 200, (service, route)
    compose('stop', *services, timeout=150)
    stopped = states()
    try:
        for service in services:
            assert stopped[service]['State'] == 'exited' and stopped[service]['ExitCode'] == 0, (service, stopped[service])
    finally:
        compose('start', *services)
    health()
    deadline = time.monotonic() + 90
    after = states()
    while time.monotonic() < deadline:
        after = states()
        if all(after[service]['Health'] == 'healthy' for service in ['postgres', 'temporal', 'nats', *services]):
            break
        time.sleep(0.5)
    assert all(after[service]['Health'] == 'healthy' for service in ['postgres', 'temporal', 'nats', *services]), after
    return {'before': before, 'graceful_exit_states': stopped, 'restarted': after, 'live_and_ready_http_200': ports,
            'all_application_graceful_exit_codes_zero': True}


def correlated_spans(logs: str, trace_id: str):
    spans = []
    current = {}
    for line in logs.splitlines():
        line = line.split(' | ', 1)[-1].strip()
        if re.fullmatch(r'Span #\d+', line):
            if current.get('Trace ID') == trace_id:
                spans.append(current)
            current = {}
        match = re.match(r'(Trace ID|Parent ID|ID|Name|Start time|End time)\s*:\s*(.*)', line)
        if match:
            current[match[1]] = match[2]
    if current.get('Trace ID') == trace_id:
        spans.append(current)
    return spans


def observability():
    trace_id = uuid.uuid4().hex
    identifier = commission('Correlated trace evidence', trace_id=trace_id)
    state = wait_state(identifier, {'WAITING_FOR_APPROVAL'})
    approve(identifier, state)
    samples = []
    for _ in range(50):
        started = time.perf_counter()
        require('/api/v1/commissions?limit=1')
        samples.append((time.perf_counter() - started) * 1000)
    # Earlier phases recreate the capability container with fault overrides, so
    # the most recent scrape can still be the one that failed while it restarted.
    # Wait for the next successful scrape of every target before asserting.
    up = {}
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        with urllib.request.urlopen(f'http://127.0.0.1:{PORTS["prometheus"]}/api/v1/query?query=up', timeout=10) as response:
            targets = json.load(response)
        assert targets['status'] == 'success'
        up = {item['metric'].get('job'): item['value'][1] for item in targets['data']['result']}
        if all(up.get(job) == '1' for job in ['velin-api', 'velin-worker', 'velin-capabilities']):
            break
        time.sleep(2)
    if not all(up.get(job) == '1' for job in ['velin-api', 'velin-worker', 'velin-capabilities']):
        with urllib.request.urlopen(f'http://127.0.0.1:{PORTS["prometheus"]}/api/v1/targets?state=active', timeout=10) as response:
            active = json.load(response)['data']['activeTargets']
        detail = {item['labels'].get('job'): {'health': item.get('health'), 'lastError': item.get('lastError'), 'scrapeUrl': item.get('scrapeUrl')} for item in active}
        raise AssertionError(clean(json.dumps({'up': up, 'targets': detail})))
    span_names = ['http.request', 'workflow.start', 'workflow.run.started', 'capability.activity', 'provider.invoke', 'tool.curated_research',
                  'persistence.step.save', 'workflow.revision.created', 'workflow.approval.requested', 'workflow.decision.recorded',
                  'artifact.production', 'persistence.artifact.save', 'workflow.workflow.completed']
    deadline = time.monotonic() + 30
    spans = []
    names = set()
    while time.monotonic() < deadline:
        logs = compose('logs', '--tail', '20000', '--no-color', 'otel', timeout=30)
        spans = correlated_spans(logs, trace_id)
        names = {span.get('Name') for span in spans}
        if all(name in names for name in span_names):
            break
        time.sleep(0.5)
    assert all(name in names for name in span_names), {'missing': sorted(set(span_names) - names), 'trace_id': trace_id}
    span_ids = {span['ID'] for span in spans}
    assert all(span.get('Parent ID') in span_ids for span in spans if span['Name'] != 'http.request'), 'Broken cross-service parent span linkage'
    metrics = {}
    for component in ['api', 'worker', 'capabilities']:
        req = urllib.request.Request(f'http://127.0.0.1:{PORTS[component]}/metrics', headers={'Authorization': 'Bearer ' + ENV['CAPABILITIES_TOKEN']})
        with urllib.request.urlopen(req, timeout=10) as response:
            metrics[component] = response.read().decode()
    for name in ['velin_workflow_outcomes_total', 'velin_workflow_duration_seconds', 'velin_approval_wait_seconds', 'velin_activity_attempts_total',
                 'velin_artifacts_total', 'velin_workflows_active', 'velin_worker_open_workflows_at_start',
                 'temporal_workflow_task_execution_latency_seconds', 'temporal_workflow_task_replay_latency_seconds']:
        assert name in metrics['worker'], name
    for name in ['velin_http_requests_total', 'velin_http_duration_seconds']:
        assert name in metrics['api'], name
    for name in ['velin_provider_calls_total', 'velin_capability_seconds', 'velin_evaluation_checks_total']:
        assert name in metrics['capabilities'], name
    return {'scope': 'Local WSL Docker on this machine, 50 authenticated sequential list requests; not cloud scale',
            'p50_ms': statistics.median(samples), 'p95_ms': sorted(samples)[47], 'prometheus_targets': up,
            'exported_span_names': sorted(names & set(span_names)), 'correlated_commission_id': identifier, 'single_trace_id': trace_id,
            'parent_linkage_verified': True, 'metric_families_present': True}


def save(name: str, data):
    OUT.mkdir(parents=True, exist_ok=True)
    text = json.dumps(data, indent=2, ensure_ascii=False)
    assert clean(text) == text, 'Verification output contains a configured secret'
    (OUT / (name + '.json')).write_text(text + '\n', encoding='utf-8')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--phase', required=True, choices=['normal', 'durability', 'crash', 'capabilities', 'security', 'outages', 'events', 'observability', 'runtime_smoke'])
    args = parser.parse_args()
    health()
    try:
        result = globals()[args.phase]()
        result.update({'executed_at_utc': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), 'status': 'PASS', 'live_external_llm': False})
        save('topology-' + args.phase, result)
        print(json.dumps({'phase': args.phase, 'status': 'PASS'}))
    except Exception as error:
        save('topology-' + args.phase + '-failure', {'status': 'FAIL', 'error': clean(repr(error))})
        raise

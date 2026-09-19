import { mount, tick, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';

const authAPI = vi.hoisted(() => ({
  acceptInvitation: vi.fn(),
  getInvitation: vi.fn(),
  requestLoginCode: vi.fn(),
  verifyLoginCode: vi.fn(),
}));

vi.mock('../api.js', () => authAPI);

import LoginPage from './LoginPage.svelte';

let component;

function mountLogin(props = {}) {
  component = mount(LoginPage, {
    target: document.body,
    props: { appName: 'CrawlObserver', onlogin: vi.fn(), ...props },
  });
  return component;
}

function setInput(selector, value) {
  const input = document.querySelector(selector);
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

afterEach(() => {
  if (component) unmount(component);
  component = undefined;
  document.body.innerHTML = '';
  window.history.replaceState({}, '', '/');
  vi.clearAllMocks();
});

describe('LoginPage passwordless authentication', () => {
  it('requests a code and allows it to be resent without any password control', async () => {
    authAPI.requestLoginCode.mockResolvedValue({ status: 'accepted' });
    mountLogin();

    expect(document.querySelector('input[type="password"]')).toBeNull();
    expect(document.body.textContent).not.toContain('Username');

    setInput('#login-email', 'person@example.test');
    await tick();
    document.querySelector('.login-form').dispatchEvent(new Event('submit', { bubbles: true }));

    await vi.waitFor(() => expect(authAPI.requestLoginCode).toHaveBeenCalledWith('person@example.test'));
    await vi.waitFor(() => expect(document.querySelector('#login-code')).not.toBeNull());
    expect(document.body.textContent).toContain('Resend code');

    document.querySelector('button[type="button"]:last-child').click();
    await vi.waitFor(() => expect(authAPI.requestLoginCode).toHaveBeenCalledTimes(2));
  });

  it('verifies a six-digit code and continues through the existing onlogin callback', async () => {
    const onlogin = vi.fn();
    authAPI.requestLoginCode.mockResolvedValue({ status: 'accepted' });
    authAPI.verifyLoginCode.mockResolvedValue({ id: 'user-1', email: 'person@example.test' });
    mountLogin({ onlogin });

    setInput('#login-email', 'person@example.test');
    await tick();
    document.querySelector('.login-form').dispatchEvent(new Event('submit', { bubbles: true }));
    await vi.waitFor(() => expect(document.querySelector('#login-code')).not.toBeNull());

    setInput('#login-code', '123456');
    await tick();
    document.querySelector('.login-form').dispatchEvent(new Event('submit', { bubbles: true }));

    await vi.waitFor(() => {
      expect(authAPI.verifyLoginCode).toHaveBeenCalledWith('person@example.test', '123456');
      expect(onlogin).toHaveBeenCalledWith({ id: 'user-1', email: 'person@example.test' });
    });
  });

  it('keeps wrong-code feedback generic when the server reports invalid or expired', async () => {
    authAPI.requestLoginCode.mockResolvedValue({ status: 'accepted' });
    authAPI.verifyLoginCode.mockRejectedValue({ status: 401, message: 'invalid or expired' });
    mountLogin();

    setInput('#login-email', 'person@example.test');
    await tick();
    document.querySelector('.login-form').dispatchEvent(new Event('submit', { bubbles: true }));
    await vi.waitFor(() => expect(document.querySelector('#login-code')).not.toBeNull());

    setInput('#login-code', '123456');
    await tick();
    document.querySelector('.login-form').dispatchEvent(new Event('submit', { bubbles: true }));

    await vi.waitFor(() =>
      expect(document.body.textContent).toContain('That code is invalid or expired'),
    );
  });

  it('shows a rate-limit response without changing to the verification step', async () => {
    authAPI.requestLoginCode.mockRejectedValue({ status: 429, message: 'rate limited' });
    mountLogin();

    setInput('#login-email', 'person@example.test');
    await tick();
    document.querySelector('.login-form').dispatchEvent(new Event('submit', { bubbles: true }));

    await vi.waitFor(() => expect(document.body.textContent).toContain('Too many attempts'));
    expect(document.querySelector('#login-code')).toBeNull();
  });

  it('inspects and accepts an invitation token from the URL', async () => {
    const onlogin = vi.fn();
    const oninvitationcomplete = vi.fn();
    window.history.replaceState({}, '', '/?invite=valid-token');
    authAPI.getInvitation.mockResolvedValue({ email: 'person@example.test' });
    authAPI.acceptInvitation.mockResolvedValue({ id: 'user-1', email: 'person@example.test' });
    mountLogin({ onlogin, oninvitationcomplete });

    await vi.waitFor(() => expect(document.body.textContent).toContain('Accept your invitation'));
    expect(authAPI.getInvitation).toHaveBeenCalledWith('valid-token');

    document.querySelector('.login-submit').click();
    await vi.waitFor(() => {
      expect(authAPI.acceptInvitation).toHaveBeenCalledWith('valid-token');
      expect(oninvitationcomplete).toHaveBeenCalledOnce();
      expect(onlogin).toHaveBeenCalledWith({ id: 'user-1', email: 'person@example.test' });
    });
  });

  it('shows an expired invitation state', async () => {
    window.history.replaceState({}, '', '/?invite=expired-token');
    authAPI.getInvitation.mockRejectedValue({ status: 410, message: 'expired invitation' });
    mountLogin();

    await vi.waitFor(() => expect(document.body.textContent).toContain('This invitation has expired'));
  });

  it('shows used and invalid invitation states without a password fallback', async () => {
    window.history.replaceState({}, '', '/?invite=used-token');
    authAPI.getInvitation.mockRejectedValue({ status: 409, message: 'invitation already used' });
    mountLogin();

    await vi.waitFor(() => expect(document.body.textContent).toContain('This invitation has already been used'));
    document.querySelector('button[type="button"]').click();
    await vi.waitFor(() => expect(document.querySelector('#login-email')).not.toBeNull());

    if (component) unmount(component);
    component = undefined;
    document.body.innerHTML = '';
    window.history.replaceState({}, '', '/?invite=invalid-token');
    authAPI.getInvitation.mockRejectedValue({ status: 404, message: 'not found' });
    mountLogin();

    await vi.waitFor(() => expect(document.body.textContent).toContain('This invitation link is invalid'));
  });

  it('shows a generic provider failure while accepting a valid invitation', async () => {
    window.history.replaceState({}, '', '/?invite=delivery-token');
    authAPI.getInvitation.mockResolvedValue({ email: 'person@example.test' });
    authAPI.acceptInvitation.mockRejectedValue({ status: 503, message: 'delivery failed' });
    mountLogin();

    await vi.waitFor(() => expect(document.body.textContent).toContain('Accept your invitation'));
    document.querySelector('.login-submit').click();

    await vi.waitFor(() =>
      expect(document.body.textContent).toContain('We could not accept this invitation'),
    );
  });
});

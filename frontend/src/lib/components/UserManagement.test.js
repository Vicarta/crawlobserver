import { mount, tick, unmount } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';

const usersAPI = vi.hoisted(() => ({
  createUser: vi.fn(),
  deleteUser: vi.fn(),
  getProjects: vi.fn(),
  getUsers: vi.fn(),
  sendUserInvitation: vi.fn(),
  updateUser: vi.fn(),
}));

vi.mock('../api.js', () => usersAPI);

import UserManagement from './UserManagement.svelte';

let component;

function setInput(selector, value) {
  const input = document.querySelector(selector);
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
}

function mountUsers(onerror = vi.fn()) {
  component = mount(UserManagement, {
    target: document.body,
    props: { onerror, onprojectschanged: vi.fn() },
  });
  return onerror;
}

function seedUsers(users = []) {
  usersAPI.getProjects.mockResolvedValue([{ id: 'project-1', name: 'Example project' }]);
  usersAPI.getUsers.mockResolvedValue(users);
  usersAPI.createUser.mockResolvedValue({ id: 'created-user' });
  usersAPI.updateUser.mockResolvedValue({});
  usersAPI.sendUserInvitation.mockResolvedValue({ status: 'sent' });
  usersAPI.deleteUser.mockResolvedValue({ status: 'deleted' });
}

afterEach(() => {
  if (component) unmount(component);
  component = undefined;
  document.body.innerHTML = '';
  vi.clearAllMocks();
});

describe('UserManagement passwordless users', () => {
  it('creates an email user and sends its invitation without password fields', async () => {
    seedUsers();
    mountUsers();

    await vi.waitFor(() => expect(document.querySelector('#user-email')).not.toBeNull());
    expect(document.querySelector('input[type="password"]')).toBeNull();
    expect(document.body.textContent).not.toContain('Username');

    setInput('#user-email', 'new@example.test');
    await tick();
    document.querySelector('.btn-primary').click();

    await vi.waitFor(() => {
      expect(usersAPI.createUser).toHaveBeenCalledWith({
        email: 'new@example.test',
        role: 'viewer',
        project_ids: [],
      });
      expect(usersAPI.sendUserInvitation).toHaveBeenCalledWith('created-user');
    });
  });

  it('updates an existing email before sending a migration invitation', async () => {
    seedUsers([
      {
        id: 'legacy-user',
        username: 'legacy-owner',
        email: '',
        role: 'viewer',
        active: true,
        project_ids: ['project-1'],
        email_verified: false,
      },
    ]);
    mountUsers();

    await vi.waitFor(() => expect(document.querySelector('#user-email-legacy-user')).not.toBeNull());
    expect(document.body.textContent).toContain('Email required');
    expect(document.body.textContent).toContain('Legacy identifier');
    expect(document.body.textContent).toContain('legacy-owner');

    setInput('#user-email-legacy-user', 'migrated@example.test');
    await tick();
    const inviteButton = [...document.querySelectorAll('.user-actions button')].find((button) =>
      button.textContent.includes('Save and send invitation'),
    );
    inviteButton.click();

    await vi.waitFor(() => {
      expect(usersAPI.updateUser).toHaveBeenCalledWith('legacy-user', {
        email: 'migrated@example.test',
        role: 'viewer',
        active: true,
        project_ids: ['project-1'],
      });
      expect(usersAPI.sendUserInvitation).toHaveBeenCalledWith('legacy-user');
    });
  });

  it('keeps the created user visible when invitation delivery fails so it can be resent', async () => {
    seedUsers();
    usersAPI.sendUserInvitation.mockRejectedValueOnce(new Error('delivery unavailable'));
    const onerror = mountUsers();

    await vi.waitFor(() => expect(document.querySelector('#user-email')).not.toBeNull());
    setInput('#user-email', 'retry@example.test');
    await tick();
    document.querySelector('.btn-primary').click();

    await vi.waitFor(() => expect(onerror).toHaveBeenCalledWith('delivery unavailable'));
    expect(usersAPI.getUsers).toHaveBeenCalledTimes(2);
  });
});

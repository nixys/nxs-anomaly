import { useEffect, useMemo, useState } from 'react';
import { Kbd, Group, Modal, Stack, Text, TextInput, UnstyledButton } from '@mantine/core';
import { useHotkeys } from '@mantine/hooks';
import { IconSearch } from '@tabler/icons-react';
import { useNavigate } from 'react-router-dom';
import { useI18n } from '../i18n/I18nProvider';
import type { StringKey } from '../i18n/I18nProvider';

export interface Command {
  id: string;
  label: string;
  /** Where it goes, or what it does. */
  to?: string;
  run?: () => void;
  /** Shown to the right, e.g. the section a page belongs to. */
  hint?: string;
}

/**
 * ⌘K over everything this installation can reach.
 *
 * The sidebar has seventeen destinations and a responder woken at night needs
 * one of them. Rather than growing the menu, the keyboard gets a single entry
 * point: type three letters of a page name and press Enter. It carries the
 * theme and language switches too, because those are the other two things
 * people hunt for in a menu.
 *
 * Written here rather than pulled from a package: it is a text input, a
 * filtered list and two arrow keys, and the alternative was a new dependency in
 * a product whose whole point is being installable behind a locked-down
 * perimeter.
 */
export function CommandPalette({ commands }: { commands: Command[] }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const [opened, setOpened] = useState(false);
  const [query, setQuery] = useState('');
  const [cursor, setCursor] = useState(0);

  useHotkeys([
    ['mod+K', () => setOpened(true)],
    // Some browsers reserve ⌘K for the address bar; Ctrl+/ is the fallback the
    // help modal advertises next to it.
    ['mod+/', () => setOpened(true)],
  ]);

  const matches = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return commands;
    return commands.filter(
      (command) =>
        command.label.toLowerCase().includes(needle) ||
        (command.hint ?? '').toLowerCase().includes(needle),
    );
  }, [commands, query]);

  useEffect(() => {
    if (cursor > matches.length - 1) setCursor(0);
  }, [matches.length, cursor]);

  const close = () => {
    setOpened(false);
    setQuery('');
    setCursor(0);
  };

  const run = (command: Command | undefined) => {
    if (!command) return;
    close();
    if (command.run) command.run();
    else if (command.to) navigate(command.to);
  };

  return (
    <Modal
      opened={opened}
      onClose={close}
      withCloseButton={false}
      size="lg"
      padding={0}
      yOffset="12vh"
      transitionProps={{ duration: 80 }}
    >
      <TextInput
        autoFocus
        variant="unstyled"
        size="md"
        px="md"
        py="xs"
        leftSection={<IconSearch size={16} />}
        placeholder={t('palette.placeholder')}
        value={query}
        onChange={(event) => {
          setQuery(event.currentTarget.value);
          setCursor(0);
        }}
        onKeyDown={(event) => {
          if (event.key === 'ArrowDown') {
            event.preventDefault();
            setCursor((prev) => Math.min(matches.length - 1, prev + 1));
          } else if (event.key === 'ArrowUp') {
            event.preventDefault();
            setCursor((prev) => Math.max(0, prev - 1));
          } else if (event.key === 'Enter') {
            event.preventDefault();
            run(matches[cursor]);
          } else if (event.key === 'Escape') {
            close();
          }
        }}
        styles={{ input: { borderBottom: '1px solid var(--mantine-color-default-border)' } }}
        aria-label={t('palette.placeholder')}
      />
      <Stack gap={0} p="xs" mah="50vh" style={{ overflowY: 'auto' }}>
        {matches.length === 0 && (
          <Text size="sm" c="dimmed" p="sm">
            {t('palette.noMatches')}
          </Text>
        )}
        {matches.map((command, index) => (
          <UnstyledButton
            key={command.id}
            onClick={() => run(command)}
            onMouseEnter={() => setCursor(index)}
            p="xs"
            style={{
              borderRadius: 'var(--mantine-radius-sm)',
              background:
                index === cursor ? 'var(--mantine-color-default-hover)' : undefined,
            }}
          >
            <Group justify="space-between" wrap="nowrap">
              <Text size="sm">{command.label}</Text>
              {command.hint && (
                <Text size="xs" c="dimmed">
                  {command.hint}
                </Text>
              )}
            </Group>
          </UnstyledButton>
        ))}
      </Stack>
      <Group gap="xs" px="md" py="xs" style={{ borderTop: '1px solid var(--mantine-color-default-border)' }}>
        <Kbd>↑</Kbd>
        <Kbd>↓</Kbd>
        <Text size="xs" c="dimmed">
          {t('palette.moveHint')}
        </Text>
        <Kbd>↵</Kbd>
        <Text size="xs" c="dimmed">
          {t('palette.openHint')}
        </Text>
      </Group>
    </Modal>
  );
}

/** Typed helper so callers can build commands from catalog keys. */
export type CommandLabelKey = StringKey;

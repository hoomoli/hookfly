import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

export const DATA_FONT_SIZE_STORAGE_KEY = "hookfly-data-font-size";
export const DATA_FONT_SIZES = ["compact", "standard", "comfortable"] as const;
export type DataFontSize = (typeof DATA_FONT_SIZES)[number];

function isDataFontSize(value: string | null): value is DataFontSize {
  return DATA_FONT_SIZES.includes(value as DataFontSize);
}

interface DisplaySettingsValue {
  dataFontSize: DataFontSize;
  setDataFontSize: (value: DataFontSize) => void;
}

const defaultValue: DisplaySettingsValue = { dataFontSize: "compact", setDataFontSize: () => undefined };
const DisplaySettingsContext = createContext<DisplaySettingsValue>(defaultValue);

export function DisplaySettingsProvider({ children, storage = window.localStorage }: { children: ReactNode; storage?: Storage }) {
  const [dataFontSize, setDataFontSize] = useState<DataFontSize>(() => {
    const stored = storage.getItem(DATA_FONT_SIZE_STORAGE_KEY);
    return isDataFontSize(stored) ? stored : "compact";
  });

  useEffect(() => {
    document.documentElement.dataset.dataFontSize = dataFontSize;
    storage.setItem(DATA_FONT_SIZE_STORAGE_KEY, dataFontSize);
  }, [dataFontSize, storage]);

  return <DisplaySettingsContext value={{ dataFontSize, setDataFontSize }}>{children}</DisplaySettingsContext>;
}

export function useDisplaySettings() {
  return useContext(DisplaySettingsContext);
}
